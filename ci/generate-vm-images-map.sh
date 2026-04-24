#!/usr/bin/env bash
#
# Build the service_key -> ghcr.io/...@sha256 map Terraform consumes via the
# vm_compose_images variable. For every entry emitted by ci/ocr-matrix.sh that
# declares a `vm_image`, resolve the current GHCR digest and include it. Emits
# a compact JSON object on stdout.
#
# The VM docker-compose stack pulls these images directly from GHCR, so VMs
# do not need Artifact Registry credentials. Cloud Run services continue to
# use ci/generate-ocr-images-map.sh (which resolves GAR digests).
#
# Required env: GCLOUD_PROJECT, WORKSPACE_SLUG, IMAGE_TAG (same contract as
# ocr-matrix.sh). Requires jq on PATH, plus docker buildx for digest lookup.

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
matrix_json="$("${repo_root}/ci/ocr-matrix.sh")"

result='{}'
while IFS= read -r entry; do
  key="$(jq -r '.key' <<<"$entry")"
  vm_image="$(jq -r '.vm_image // ""' <<<"$entry")"
  if [ -z "$vm_image" ]; then
    continue
  fi

  resolved="$("${repo_root}/ci/resolve-ghcr-image.sh" "$vm_image")"
  result="$(jq --arg k "$key" --arg v "$resolved" '. + {($k): $v}' <<<"$result")"
done < <(jq -c '.include[]' <<<"$matrix_json")

jq -c . <<<"$result"
