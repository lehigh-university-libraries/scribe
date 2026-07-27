#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d -- "${TMPDIR:-/tmp}/scribe-cloud-ocr-preflight.XXXXXXXXXX")"
cleanup() {
  rm -rf -- "${TEST_DIR}"
}
trap cleanup EXIT

mkdir -p -- "${TEST_DIR}/bin"
FAKE_COMPOSE_JSON="${TEST_DIR}/compose.json"
export FAKE_COMPOSE_JSON

# The fake exposes only the one read-only Compose operation used by the
# preflight, so the contract cannot accidentally start a container.
cat >"${TEST_DIR}/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[ "$#" -eq 4 ]
[ "$1" = "compose" ]
[ "$2" = "config" ]
[ "$3" = "--format" ]
[ "$4" = "json" ]
command cat -- "${FAKE_COMPOSE_JSON}"
EOF
chmod 0755 -- "${TEST_DIR}/bin/docker"

project="example-project"
origin="https://scribe-dev-us-central1.run.app"
endpoint_map="$(jq -cn --arg origin "${origin}" '{default: {url: $origin, audience: $origin}}')"

write_valid_config() {
  jq -n \
    --arg project "${project}" \
    --arg origin "${origin}" \
    --arg endpoint_map "${endpoint_map}" '
      {
        services: {
          api: {environment: {
            GCLOUD_PROJECT: $project,
            OLLAMA_URL: $origin,
            OLLAMA_AUDIENCE: $origin,
            OLLAMA_MODEL_ENDPOINTS_JSON: $endpoint_map,
            SEGMENTATION_SERVICE_URL: $origin,
            SEGMENTATION_SERVICE_AUDIENCE: $origin,
            SEGMENTATION_MODEL_ENDPOINTS_JSON: $endpoint_map,
            KRAKEN_MODEL_ENDPOINTS_JSON: $endpoint_map
          }},
          worker: {environment: {
            GCLOUD_PROJECT: $project,
            OLLAMA_URL: $origin,
            OLLAMA_AUDIENCE: $origin,
            OLLAMA_MODEL_ENDPOINTS_JSON: $endpoint_map,
            SEGMENTATION_SERVICE_URL: $origin,
            SEGMENTATION_SERVICE_AUDIENCE: $origin,
            SEGMENTATION_MODEL_ENDPOINTS_JSON: $endpoint_map,
            KRAKEN_MODEL_ENDPOINTS_JSON: $endpoint_map
          }}
        }
      }
    ' >"${FAKE_COMPOSE_JSON}"
}

run_preflight() {
  PATH="${TEST_DIR}/bin:${PATH}" \
    "${ROOT_DIR}/ci/cloud-ocr-compose-preflight.sh" "$@"
}

assert_rejected() {
  local name="$1"
  if run_preflight --print-project >"${TEST_DIR}/${name}.out" 2>"${TEST_DIR}/${name}.err"; then
    echo "cloud OCR preflight accepted invalid config: ${name}" >&2
    exit 1
  fi
  [ ! -s "${TEST_DIR}/${name}.out" ]
}

write_valid_config
[ "$(run_preflight --print-project)" = "${project}" ]
[ "$(run_preflight)" = \
  "Cloud OCR Compose configuration is project- and endpoint-bound with no local segmentor." ]

jq '.services.worker.environment.GCLOUD_PROJECT = "different-project"' \
  "${FAKE_COMPOSE_JSON}" >"${TEST_DIR}/candidate.json"
mv -- "${TEST_DIR}/candidate.json" "${FAKE_COMPOSE_JSON}"
assert_rejected mismatched-project

write_valid_config
jq 'del(.services.api.environment.GCLOUD_PROJECT)' \
  "${FAKE_COMPOSE_JSON}" >"${TEST_DIR}/candidate.json"
mv -- "${TEST_DIR}/candidate.json" "${FAKE_COMPOSE_JSON}"
assert_rejected missing-project

write_valid_config
jq '.services.api.environment.GCLOUD_PROJECT = "INVALID" |
    .services.worker.environment.GCLOUD_PROJECT = "INVALID"' \
  "${FAKE_COMPOSE_JSON}" >"${TEST_DIR}/candidate.json"
mv -- "${TEST_DIR}/candidate.json" "${FAKE_COMPOSE_JSON}"
assert_rejected invalid-project

write_valid_config
jq '.services.segmentor = {}' \
  "${FAKE_COMPOSE_JSON}" >"${TEST_DIR}/candidate.json"
mv -- "${TEST_DIR}/candidate.json" "${FAKE_COMPOSE_JSON}"
assert_rejected local-segmentor

echo "Cloud OCR Compose project binding passed."
