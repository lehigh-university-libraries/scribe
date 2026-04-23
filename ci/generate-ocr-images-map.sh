#!/usr/bin/env bash
#
# Build the service_key -> image@sha256 map Terraform consumes via the
# ocr_service_images variable. For each entry emitted by ci/ocr-matrix.sh,
# resolve the current GAR digest for the tagged image reference. Emits a
# compact JSON object on stdout.
#
# Required env: GCLOUD_PROJECT, WORKSPACE_SLUG, IMAGE_TAG (same contract as
# ocr-matrix.sh). Requires gcloud + yq + jq on PATH.

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
matrix_json="$("${repo_root}/ci/ocr-matrix.sh")"

result='{}'
while IFS= read -r entry; do
  key="$(jq -r '.key' <<<"$entry")"
  image="$(jq -r '.image' <<<"$entry")"
  resolved="$("${repo_root}/ci/resolve-gar-image.sh" "$image")"
  result="$(jq --arg k "$key" --arg v "$resolved" '. + {($k): $v}' <<<"$result")"
done < <(jq -c '.include[]' <<<"$matrix_json")

jq -c . <<<"$result"
