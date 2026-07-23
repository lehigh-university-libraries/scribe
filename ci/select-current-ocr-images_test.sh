#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fixture="${repo_root}/ci/fixtures/ocr-multi-model.yaml"
test_root="$(mktemp -d)"
trap 'rm -rf "${test_root}"' EXIT

image() {
  printf 'us-docker.pkg.dev/scribe-test/internal/%s@sha256:%064d' "$1" 1
}

recorded="$(
  jq -n \
    --arg segmentor "$(image scribe-segmentor)" \
    --arg seg_default "$(image scribe-ks-default)" \
    --arg seg_v2 "$(image scribe-ks-v2)" \
    --arg tx_default "$(image scribe-ko-default)" \
    --arg tx_v2 "$(image scribe-ko-v2)" \
    --arg ollama "$(image ollama-vision-default)" \
    --arg obsolete "$(image obsolete-image-service)" \
    '{
      "segmentor": $segmentor,
      "kraken-seg/layout-default": $seg_default,
      "kraken-seg/layout-lines-v2": $seg_v2,
      "kraken-ocr/handwriting-default": $tx_default,
      "kraken-ocr/latin-handwriting-v2": $tx_v2,
      "ollama/vision-default:latest": $ollama,
      "image-service": $obsolete
    }'
)"

selected="$(
  GCLOUD_PROJECT=scribe-test \
    WORKSPACE_SLUG=prod \
    CONFIG_PATH="${fixture}" \
    "${repo_root}/ci/select-current-ocr-images.sh" <<<"${recorded}"
)"
jq -e '
  (keys | length) == 6 and
  has("image-service") == false and
  has("ollama/vision-default:latest")
' <<<"${selected}" >/dev/null

without_ollama="$(
  GCLOUD_PROJECT=scribe-test \
    WORKSPACE_SLUG=dev \
    INCLUDE_OLLAMA=false \
    CONFIG_PATH="${fixture}" \
    "${repo_root}/ci/select-current-ocr-images.sh" <<<"${recorded}"
)"
jq -e '
  (keys | length) == 5 and
  all(keys[]; startswith("ollama/") | not)
' <<<"${without_ollama}" >/dev/null

missing="$(
  jq 'del(."kraken-ocr/latin-handwriting-v2")' <<<"${recorded}"
)"
if GCLOUD_PROJECT=scribe-test WORKSPACE_SLUG=prod CONFIG_PATH="${fixture}" \
  "${repo_root}/ci/select-current-ocr-images.sh" <<<"${missing}" >/dev/null 2>&1; then
  echo "OCR rollback selection accepted a missing current service image" >&2
  exit 1
fi

echo "Recorded OCR image selection projects exactly onto the current service matrix."
