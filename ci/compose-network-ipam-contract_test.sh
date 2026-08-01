#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "Compose network contract failed: $*" >&2
  exit 1
}

command -v docker >/dev/null 2>&1 || fail "Docker with the Compose plugin is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"

base_config="$(
  docker compose \
    --project-directory "$ROOT_DIR" \
    -f "$ROOT_DIR/docker-compose.yaml" \
    --profile init \
    config --format json
)"
jq -e '
  (.networks.default.ipam.config // []) == [] and
  (.services.traefik.networks.default.ipv4_address // "") == "" and
  .services.api.environment.SERVER_TRUSTED_PROXY_HOSTS == "traefik" and
  .services.worker.environment.SERVER_TRUSTED_PROXY_HOSTS == "traefik"
' <<<"$base_config" >/dev/null ||
  fail "base Compose must use automatic IPAM and the exact Traefik service identity"

# The cloud-only overlay retains the previous fixed tuple for one rollback
# generation. New application code does not use its /32; old source can still
# be restored before this compatibility overlay is retired.
merged_config="$(
  docker compose \
    --project-directory "$ROOT_DIR" \
    -f "$ROOT_DIR/docker-compose.yaml" \
    -f "$ROOT_DIR/terraform/rootfs/home/cloud-compose/scribe-runtime.compose.yaml" \
    --profile init \
    config --format json
)"
jq -e '
  .networks.default.ipam.config == [{
    subnet: "172.30.0.0/24",
    ip_range: "172.30.0.128/25",
    gateway: "172.30.0.1"
  }] and
  .services.api.environment.SERVER_TRUSTED_PROXY_HOSTS == "traefik"
' <<<"$merged_config" >/dev/null ||
  fail "cloud rollback overlay no longer supplies its reviewed legacy tuple"

bash scripts/validate-compose-runtime.sh >/dev/null

echo "Local Compose uses automatic IPAM; the fixed cloud tuple is rollback-only."
