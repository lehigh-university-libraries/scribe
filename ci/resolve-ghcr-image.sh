#!/usr/bin/env bash
#
# Resolve a GHCR image reference with a tag to a digest-pinned reference:
#   ghcr.io/owner/name:tag -> ghcr.io/owner/name@sha256:...
#
# If the input already contains @sha256:, it is echoed back unchanged.
# Relies on `docker buildx imagetools inspect`, which is available on any
# runner with Docker + buildx (the github-hosted ubuntu-* images).

set -euo pipefail

image_ref="${1:-}"

if [ -z "$image_ref" ]; then
  echo "usage: $0 <ghcr-image:tag|ghcr-image@sha256:digest>" >&2
  exit 1
fi

if [[ "$image_ref" =~ ^ghcr\.io/[a-z0-9._/-]+@sha256:[0-9a-f]{64}$ ]]; then
  printf '%s\n' "$image_ref"
  exit 0
fi
if [[ ! "$image_ref" =~ ^ghcr\.io/[a-z0-9._/-]+:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$ ]]; then
  echo "expected a ghcr.io image reference with a tag or digest, got: $image_ref" >&2
  exit 1
fi

digest="$(docker buildx imagetools inspect "$image_ref" --format '{{json .Manifest}}' \
  | jq -r '.digest')"

if [[ ! "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo "failed to resolve digest for ghcr image: $image_ref" >&2
  exit 1
fi

printf '%s@%s\n' "${image_ref%:*}" "$digest"
