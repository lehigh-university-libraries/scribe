#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "Compose network test failed: $*" >&2
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


echo "Local Compose uses automatic IPAM and the Traefik service identity."
