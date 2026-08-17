#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OCR_CONFIG="${ROOT_DIR}/config/ocr.yaml"
COMPOSE_OVERRIDE="${ROOT_DIR}/docker-compose.override-example.yaml"
CLOUD_COMPOSE_OVERRIDE="${ROOT_DIR}/docker-compose.override.cloud-example.yaml"

fail() {
  echo "OCR local defaults test failed: $*" >&2
  exit 1
}

require_value() {
  local label="$1"
  local value="$2"
  [ -n "${value}" ] || fail "${label} is empty"
}

assert_equal() {
  local label="$1"
  local expected="$2"
  local actual="$3"
  [ "${actual}" = "${expected}" ] ||
    fail "${label} is ${actual@Q}; expected ${expected@Q} from config/ocr.yaml"
}


default_segmentation_model="$(yq -r '.kraken.default_segmentation_model // ""' "${OCR_CONFIG}")"
default_transcription_model="$(yq -r '.kraken.default_transcription_model // ""' "${OCR_CONFIG}")"
kraken_pip_spec="$(yq -r '.kraken.pip_spec // ""' "${OCR_CONFIG}")"
require_value "kraken.default_segmentation_model" "${default_segmentation_model}"
require_value "kraken.default_transcription_model" "${default_transcription_model}"
require_value "kraken.pip_spec" "${kraken_pip_spec}"

segmentation_file="$(
  MODEL="${default_segmentation_model}" \
    yq -r '.kraken.segmentation_models[strenv(MODEL)].file // ""' "${OCR_CONFIG}"
)"
segmentation_doi="$(
  MODEL="${default_segmentation_model}" \
    yq -r '.kraken.segmentation_models[strenv(MODEL)].doi // ""' "${OCR_CONFIG}"
)"
segmentation_sha256="$(
  MODEL="${default_segmentation_model}" \
    yq -r '.kraken.segmentation_models[strenv(MODEL)].sha256 // ""' "${OCR_CONFIG}"
)"
transcription_file="$(
  MODEL="${default_transcription_model}" \
    yq -r '.kraken.transcription_models[strenv(MODEL)].file // ""' "${OCR_CONFIG}"
)"
transcription_doi="$(
  MODEL="${default_transcription_model}" \
    yq -r '.kraken.transcription_models[strenv(MODEL)].doi // ""' "${OCR_CONFIG}"
)"
transcription_sha256="$(
  MODEL="${default_transcription_model}" \
    yq -r '.kraken.transcription_models[strenv(MODEL)].sha256 // ""' "${OCR_CONFIG}"
)"
for spec in \
  "default segmentation file:${segmentation_file}" \
  "default segmentation DOI:${segmentation_doi}" \
  "default segmentation SHA-256:${segmentation_sha256}" \
  "default transcription file:${transcription_file}" \
  "default transcription DOI:${transcription_doi}" \
  "default transcription SHA-256:${transcription_sha256}"; do
  require_value "${spec%%:*}" "${spec#*:}"
done


compose_dockerfile="$(yq -r '.services.segmentor.build.dockerfile // ""' "${COMPOSE_OVERRIDE}")"
[ "${compose_dockerfile}" = "Dockerfile.segmentor" ] ||
  fail "local segmentor must build Dockerfile.segmentor"

api_segmentation_endpoints="$(
  yq -r '.services.api.environment.SEGMENTATION_MODEL_ENDPOINTS_JSON // ""' "${COMPOSE_OVERRIDE}"
)"
worker_segmentation_endpoints="$(
  yq -r '.services.worker.environment.SEGMENTATION_MODEL_ENDPOINTS_JSON // ""' "${COMPOSE_OVERRIDE}"
)"
api_transcription_endpoints="$(
  yq -r '.services.api.environment.KRAKEN_MODEL_ENDPOINTS_JSON // ""' "${COMPOSE_OVERRIDE}"
)"
worker_transcription_endpoints="$(
  yq -r '.services.worker.environment.KRAKEN_MODEL_ENDPOINTS_JSON // ""' "${COMPOSE_OVERRIDE}"
)"

[ "${api_segmentation_endpoints}" = "${worker_segmentation_endpoints}" ] ||
  fail "API and worker local segmentation endpoint maps differ"
[ "${api_transcription_endpoints}" = "${worker_transcription_endpoints}" ] ||
  fail "API and worker local transcription endpoint maps differ"

jq -e --arg model "${default_segmentation_model}" '
  type == "object" and
  keys == [$model] and
  .[$model] == {"url": "http://segmentor:8080", "audience": ""}
' <<<"${api_segmentation_endpoints}" >/dev/null ||
  fail "local segmentation endpoint map must expose exactly the configured default"

jq -e --arg model "${default_transcription_model}" '
  type == "object" and
  keys == [$model] and
  .[$model] == {"url": "http://segmentor:8080", "audience": ""}
' <<<"${api_transcription_endpoints}" >/dev/null ||
  fail "local transcription endpoint map must expose exactly the configured default"

[ -f "${CLOUD_COMPOSE_OVERRIDE}" ] ||
  fail "cloud OCR Compose override is missing"
[ "$(yq -r '.services.segmentor == null' "${CLOUD_COMPOSE_OVERRIDE}")" = "true" ] ||
  fail "cloud OCR Compose override must not define a local segmentor service"

for service in api worker; do
  [ "$(SERVICE="${service}" yq -r '.services[strenv(SERVICE)].depends_on.segmentor == null' "${CLOUD_COMPOSE_OVERRIDE}")" = "true" ] ||
    fail "cloud OCR ${service} must not depend on the local segmentor"
done

api_cloud_project="$(yq -r '.services.api.environment.GCLOUD_PROJECT // ""' "${CLOUD_COMPOSE_OVERRIDE}")"
worker_cloud_project="$(yq -r '.services.worker.environment.GCLOUD_PROJECT // ""' "${CLOUD_COMPOSE_OVERRIDE}")"
assert_equal "cloud OCR API/worker GCP project" "${api_cloud_project}" "${worker_cloud_project}"
project_required_prefix="\${GCLOUD_PROJECT:?"
[[ "${api_cloud_project}" == "${project_required_prefix}"* ]] ||
  fail "cloud OCR GCLOUD_PROJECT must be explicitly supplied instead of accepting an unbound credential"

for key in \
  OLLAMA_URL \
  OLLAMA_AUDIENCE \
  OLLAMA_MODEL_ENDPOINTS_JSON \
  SEGMENTATION_SERVICE_URL \
  SEGMENTATION_SERVICE_AUDIENCE \
  SEGMENTATION_MODEL_ENDPOINTS_JSON \
  KRAKEN_MODEL_ENDPOINTS_JSON; do
  api_value="$(KEY="${key}" yq -r '.services.api.environment[strenv(KEY)] // ""' "${CLOUD_COMPOSE_OVERRIDE}")"
  worker_value="$(KEY="${key}" yq -r '.services.worker.environment[strenv(KEY)] // ""' "${CLOUD_COMPOSE_OVERRIDE}")"
  assert_equal "cloud OCR API/worker ${key}" "${api_value}" "${worker_value}"
  required_prefix="\${${key}:?"
  [[ "${api_value}" == "${required_prefix}"* ]] ||
    fail "cloud OCR ${key} must be explicitly supplied instead of falling back to a local endpoint"
done


echo "OCR local defaults remain aligned."
