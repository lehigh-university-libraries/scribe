#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PRINT_PROJECT=false

fail() {
  echo "Cloud OCR Compose preflight failed: $*" >&2
  exit 1
}

case "$#" in
  0) ;;
  1)
    [ "$1" = "--print-project" ] ||
      fail "usage: $0 [--print-project]"
    PRINT_PROJECT=true
    ;;
  *) fail "usage: $0 [--print-project]" ;;
esac

cd "${ROOT_DIR}"
compose_json="$(docker compose config --format json)" ||
  fail "resolve the active Compose configuration; set every cloud OCR variable documented in docs/getting-started/local-development.md"

jq -e '
  def project_id:
    type == "string" and
    test("^[a-z][a-z0-9-]{4,28}[a-z0-9]$");
  def cloud_run_origin:
    type == "string" and
    test("^https://[^/?#[:space:]:]+[.]run[.]app$");
  def endpoint_map:
    (fromjson? // null) as $map |
    ($map | type == "object" and length > 0) and
    all($map[]; (.url | cloud_run_origin) and (.audience == .url));
  [
    "OLLAMA_URL",
    "OLLAMA_AUDIENCE",
    "OLLAMA_MODEL_ENDPOINTS_JSON",
    "SEGMENTATION_SERVICE_URL",
    "SEGMENTATION_SERVICE_AUDIENCE",
    "SEGMENTATION_MODEL_ENDPOINTS_JSON",
    "KRAKEN_MODEL_ENDPOINTS_JSON"
  ] as $keys |
  .services.api.environment as $api |
  .services.worker.environment as $worker |
  (.services | has("segmentor") | not) and
  all($keys[]; $api[.] == $worker[.]) and
  ($api.GCLOUD_PROJECT | project_id) and
  ($worker.GCLOUD_PROJECT == $api.GCLOUD_PROJECT) and
  ($api.OLLAMA_URL | cloud_run_origin) and
  ($api.OLLAMA_AUDIENCE == $api.OLLAMA_URL) and
  ($api.SEGMENTATION_SERVICE_URL | cloud_run_origin) and
  ($api.SEGMENTATION_SERVICE_AUDIENCE == $api.SEGMENTATION_SERVICE_URL) and
  ($api.OLLAMA_MODEL_ENDPOINTS_JSON | endpoint_map) and
  ($api.SEGMENTATION_MODEL_ENDPOINTS_JSON | endpoint_map) and
  ($api.KRAKEN_MODEL_ENDPOINTS_JSON | endpoint_map)
' <<<"${compose_json}" >/dev/null ||
  fail "the active override must omit segmentor and configure one canonical GCP project plus matching private https://...run.app URL/audience pairs for API and worker"

if [ "${PRINT_PROJECT}" = "true" ]; then
  jq -r '.services.api.environment.GCLOUD_PROJECT' <<<"${compose_json}"
else
  echo "Cloud OCR Compose configuration is project- and endpoint-bound with no local segmentor."
fi
