#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OCR_CONFIG="${ROOT_DIR}/config/ocr.yaml"
SEGMENTOR_DOCKERFILE="${ROOT_DIR}/Dockerfile.segmentor"
COMPOSE_OVERRIDE="${ROOT_DIR}/docker-compose.override-example.yaml"

fail() {
  echo "OCR local defaults contract failed: $*" >&2
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

docker_arg() {
  local name="$1"
  local -a values=()
  mapfile -t values < <(sed -n "s/^ARG ${name}=//p" "${SEGMENTOR_DOCKERFILE}")
  [ "${#values[@]}" -eq 1 ] ||
    fail "${SEGMENTOR_DOCKERFILE##*/} must declare ARG ${name} exactly once"
  require_value "ARG ${name}" "${values[0]}"
  printf '%s' "${values[0]}"
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

assert_equal "Dockerfile Kraken package" "${kraken_pip_spec}" \
  "$(docker_arg KRAKEN_PIP_SPEC)"
assert_equal "Dockerfile default segmentation model ID" "${default_segmentation_model}" \
  "$(docker_arg KRAKEN_SEGMENTATION_MODEL_ID)"
assert_equal "Dockerfile default segmentation model file" "${segmentation_file}" \
  "$(docker_arg KRAKEN_SEGMENTATION_MODEL_FILE)"
assert_equal "Dockerfile default segmentation model DOI" "${segmentation_doi}" \
  "$(docker_arg KRAKEN_SEGMENTATION_MODEL_DOI)"
assert_equal "Dockerfile default segmentation model SHA-256" "${segmentation_sha256}" \
  "$(docker_arg KRAKEN_SEGMENTATION_MODEL_SHA256)"
assert_equal "Dockerfile default transcription model ID" "${default_transcription_model}" \
  "$(docker_arg KRAKEN_TRANSCRIPTION_MODEL_ID)"
assert_equal "Dockerfile default transcription model file" "${transcription_file}" \
  "$(docker_arg KRAKEN_RECOGNITION_MODEL_FILE)"
assert_equal "Dockerfile default transcription model DOI" "${transcription_doi}" \
  "$(docker_arg KRAKEN_RECOGNITION_MODEL_DOI)"
assert_equal "Dockerfile default transcription model SHA-256" "${transcription_sha256}" \
  "$(docker_arg KRAKEN_RECOGNITION_MODEL_SHA256)"

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

echo "OCR local defaults remain aligned."
