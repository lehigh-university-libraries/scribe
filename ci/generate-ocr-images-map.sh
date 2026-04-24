#!/usr/bin/env bash
#
# Build the service_key -> image@sha256 map Terraform consumes via the
# ocr_service_images variable. For each entry emitted by ci/ocr-matrix.sh,
# resolve the current GAR digest for the tagged image reference. Emits a
# compact JSON object on stdout.
#
# Required env: GCLOUD_PROJECT, WORKSPACE_SLUG, IMAGE_TAG (same contract as
# ocr-matrix.sh). Requires gcloud + yq + jq on PATH, plus docker when
# AUTO_BUILD_MISSING=true.
#
# Optional env:
#   INCLUDE_OLLAMA      true|false. Defaults to true. Set false for local
#                       dev/preview applies, which do not deploy shared Ollama
#                       services and should not require those large images.
#   AUTO_BUILD_MISSING  true|false. Defaults to false. When true, missing GAR
#                       tags are built and pushed locally before digest
#                       resolution continues.

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
matrix_json="$("${repo_root}/ci/ocr-matrix.sh")"
include_ollama="${INCLUDE_OLLAMA:-true}"
auto_build_missing="${AUTO_BUILD_MISSING:-false}"

if [ "$include_ollama" != "true" ]; then
  matrix_json="$(jq -c '{include: [.include[] | select((.key | startswith("ollama/")) | not)]}' <<<"$matrix_json")"
fi

resolve_entry_image() {
  local entry="$1"
  local key image context file platform build_args resolved

  key="$(jq -r '.key' <<<"$entry")"
  image="$(jq -r '.image' <<<"$entry")"

  if resolved="$("${repo_root}/ci/resolve-gar-image.sh" "$image" 2>&1)"; then
    printf '%s\n' "$resolved"
    return 0
  fi

  echo "$resolved" >&2

  if [ "$auto_build_missing" != "true" ]; then
    echo "Missing OCR GAR image for '${key}': ${image}" >&2
    echo "Publish it first, or rerun with AUTO_BUILD_MISSING=true." >&2
    return 1
  fi

  context="$(jq -r '.context' <<<"$entry")"
  file="$(jq -r '.file' <<<"$entry")"
  platform="$(jq -r '.platform // "linux/amd64"' <<<"$entry")"
  build_args="$(jq -r '.build_args // ""' <<<"$entry")"

  cmd=(
    "${repo_root}/ci/build-push-gar-image.sh"
    --image "$image"
    --context "${repo_root}/${context}"
    --platform "$platform"
  )

  if [ -n "$file" ] && [ "$file" != "null" ]; then
    cmd+=(--file "${repo_root}/${file}")
  fi

  if [ -n "$build_args" ]; then
    while IFS= read -r build_arg; do
      if [ -n "$build_arg" ]; then
        cmd+=(--build-arg "$build_arg")
      fi
    done <<<"$build_args"
  fi

  echo "GAR image missing for ${key}; building and pushing ${image} locally..." >&2
  "${cmd[@]}" >&2

  "${repo_root}/ci/resolve-gar-image.sh" "$image"
}

result='{}'
while IFS= read -r entry; do
  key="$(jq -r '.key' <<<"$entry")"
  resolved="$(resolve_entry_image "$entry")"
  result="$(jq --arg k "$key" --arg v "$resolved" '. + {($k): $v}' <<<"$result")"
done < <(jq -c '.include[]' <<<"$matrix_json")

jq -c . <<<"$result"
