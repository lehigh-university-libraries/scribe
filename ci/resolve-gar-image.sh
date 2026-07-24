#!/usr/bin/env bash

set -euo pipefail

image_ref="${1:-}"

if [ -z "$image_ref" ]; then
  echo "usage: $0 <artifact-registry-image[:tag]|artifact-registry-image@sha256:digest>" >&2
  exit 1
fi

if [[ "$image_ref" =~ ^us-docker\.pkg\.dev/[a-z0-9._/-]+@sha256:[0-9a-f]{64}$ ]]; then
  printf '%s\n' "$image_ref"
  exit 0
fi
if [[ ! "$image_ref" =~ ^us-docker\.pkg\.dev/[a-z0-9._/-]+:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$ ]]; then
  echo "expected an Artifact Registry image reference with a tag or digest, got: $image_ref" >&2
  exit 1
fi

image="${image_ref%:*}"
tag="${image_ref##*:}"
stderr_file="$(mktemp)"
trap 'rm -f "$stderr_file"' EXIT

# The image-description command also queries Container Analysis metadata and
# therefore needs project-wide occurrence access. Listing tags stays within the
# repository-scoped Artifact Registry role used by local and CI deploys.
if ! tags_json="$(
  gcloud artifacts docker tags list "$image" \
    --format='json(image,tag,version)' 2>"$stderr_file"
)"; then
  err_output="$(cat "$stderr_file")"
  if [ -n "$err_output" ]; then
    echo "$err_output" >&2
  fi
  echo "failed to resolve Artifact Registry digest for: $image_ref" >&2
  exit 1
fi

if ! digest="$(
  jq -er \
    --arg image "$image" \
    --arg tag_suffix "/tags/${tag}" \
    '[
      .[]
      | select(
          .image == $image
          and (.tag | type == "string")
          and (.tag | endswith($tag_suffix))
          and (.version | type == "string")
        )
      | .version
      | split("/versions/")
      | if length == 2 and .[0] != "" and .[1] != "" then .[1] else empty end
    ]
    | if length == 1 then .[0] else empty end' \
    <<<"$tags_json"
)"; then
  echo "failed to resolve digest for Artifact Registry image: $image_ref" >&2
  exit 1
fi

if [[ ! "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo "failed to resolve digest for Artifact Registry image: $image_ref" >&2
  exit 1
fi

printf '%s@%s\n' "$image" "$digest"
