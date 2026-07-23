#!/usr/bin/env bash

set -euo pipefail

image_ref="${1:?digest-pinned image reference is required}"
platform="${2:-linux/amd64}"
manifest_fixture="${3:-}"

[[ "$image_ref" =~ ^[^[:space:]@]+@sha256:[0-9a-f]{64}$ ]] || {
  echo "image reference must be digest-pinned" >&2
  exit 2
}
[[ "$platform" =~ ^[a-z0-9._-]+/[a-z0-9._-]+$ ]] || {
  echo "platform must use os/architecture form" >&2
  exit 2
}

image_repository="${image_ref%@sha256:*}"
expected_digest="${image_ref##*@}"
platform_os="${platform%%/*}"
platform_arch="${platform#*/}"

cleanup_manifest=false
if [ -n "$manifest_fixture" ]; then
  [ -r "$manifest_fixture" ] || {
    echo "manifest fixture is not readable" >&2
    exit 2
  }
  manifest_file="$manifest_fixture"
else
  command -v docker >/dev/null 2>&1 || {
    echo "docker with buildx is required to resolve an OCI platform image" >&2
    exit 127
  }
  manifest_file="$(mktemp)"
  cleanup_manifest=true
  trap 'rm -f "$manifest_file"' EXIT
  if ! docker buildx imagetools inspect "$image_ref" --raw >"$manifest_file"; then
    registry="${image_repository%%/*}"
    if [[ "$registry" =~ ^[a-z0-9-]+-docker\.pkg\.dev$ ]] && command -v gcloud >/dev/null 2>&1; then
      gcloud auth print-access-token |
        docker login --username oauth2accesstoken --password-stdin "$registry" >/dev/null
      docker buildx imagetools inspect "$image_ref" --raw >"$manifest_file"
    else
      echo "failed to inspect the OCI manifest" >&2
      exit 1
    fi
  fi
fi

actual_digest="sha256:$(sha256sum "$manifest_file" | awk '{print $1}')"
[ "$actual_digest" = "$expected_digest" ] || {
  echo "resolved manifest bytes do not match the requested image digest" >&2
  exit 1
}

media_type="$(jq -er '.mediaType' "$manifest_file")"
case "$media_type" in
  application/vnd.oci.image.manifest.v1+json | application/vnd.docker.distribution.manifest.v2+json)
    runtime_digest="$expected_digest"
    ;;
  application/vnd.oci.image.index.v1+json | application/vnd.docker.distribution.manifest.list.v2+json)
    runtime_digest="$(
      jq -er --arg os "$platform_os" --arg arch "$platform_arch" '
        [
          .manifests[]?
          | select(.platform.os == $os and .platform.architecture == $arch)
          | .digest
        ]
        | if length == 1 then .[0]
          else error("expected exactly one runnable descriptor for \($os)/\($arch)")
          end
      ' "$manifest_file"
    )"
    ;;
  *)
    echo "unsupported OCI manifest media type: $media_type" >&2
    exit 1
    ;;
esac

[[ "$runtime_digest" =~ ^sha256:[0-9a-f]{64}$ ]] || {
  echo "resolved platform descriptor is not digest-pinned" >&2
  exit 1
}

printf '%s@%s\n' "$image_repository" "$runtime_digest"

if [ "$cleanup_manifest" = true ]; then
  rm -f "$manifest_file"
  trap - EXIT
fi
