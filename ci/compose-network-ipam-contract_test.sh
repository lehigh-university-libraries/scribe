#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"
TEST_DIR="$(mktemp -d "${TMPDIR:-/tmp}/scribe-compose-ipam-test.XXXXXX")"
trap 'rm -rf -- "$TEST_DIR"' EXIT

fail() {
  echo "Compose network IPAM contract failed: $*" >&2
  exit 1
}

command -v docker >/dev/null 2>&1 || fail "Docker with the Compose plugin is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"

rg -q 'compose_dynamic_ip_range[[:space:]]*=[[:space:]]*cidrsubnet\(var\.compose_network_cidr, 1, 1\)' \
  terraform/main.tf || fail "Terraform does not derive the upper-half dynamic pool"
rg -q 'compose_gateway_ip[[:space:]]*=[[:space:]]*cidrhost\(var\.compose_network_cidr, 1\)' \
  terraform/main.tf || fail "Terraform does not derive the fixed bridge gateway"
rg -q 'name[[:space:]]*=[[:space:]]*"SCRIBE_COMPOSE_IP_RANGE"' terraform/main.tf ||
  fail "Terraform does not inject the dynamic allocation range"
rg -q 'name[[:space:]]*=[[:space:]]*"SCRIBE_COMPOSE_GATEWAY"' terraform/main.tf ||
  fail "Terraform does not inject the fixed gateway"
rg -q 'regexall\("/\(2\[4-8\]\)\$", var\.compose_network_cidr\)' terraform/variables.tf ||
  fail "Terraform does not reject Compose subnets smaller than /28"

render_and_assert() {
  local subnet="$1" gateway="$2" dynamic_range="$3" traefik="$4"
  local config merged_config

  config="$(
    SCRIBE_COMPOSE_SUBNET="$subnet" \
      SCRIBE_COMPOSE_GATEWAY="$gateway" \
      SCRIBE_COMPOSE_IP_RANGE="$dynamic_range" \
      SCRIBE_TRAEFIK_IP="$traefik" \
      SERVER_TRUSTED_PROXY_CIDRS="${traefik}/32" \
      docker compose \
        --project-directory "$ROOT_DIR" \
        -f "$ROOT_DIR/docker-compose.yaml" \
        --profile init \
        config --format json
  )"
  jq -e \
    --arg subnet "$subnet" \
    --arg gateway "$gateway" \
    --arg dynamic "$dynamic_range" \
    --arg traefik "$traefik" \
    '
      .networks.default.ipam.config == [{
        subnet: $subnet,
        gateway: $gateway,
        ip_range: $dynamic
      }] and
      .services.traefik.networks.default.ipv4_address == $traefik and
      ([.services | to_entries[] |
        select((.value.networks.default.ipv4_address // "") == "")] | length) == 5 and
      (.networks.default.ipam.config[0] | has("aux_addresses") | not)
    ' <<<"$config" >/dev/null ||
    fail "rendered IPAM does not match ${subnet}"

  merged_config="$(
    SCRIBE_COMPOSE_SUBNET="$subnet" \
      SCRIBE_COMPOSE_GATEWAY="$gateway" \
      SCRIBE_COMPOSE_IP_RANGE="$dynamic_range" \
      SCRIBE_TRAEFIK_IP="$traefik" \
      SERVER_TRUSTED_PROXY_CIDRS="${traefik}/32" \
      docker compose \
        --project-directory "$ROOT_DIR" \
        -f "$ROOT_DIR/docker-compose.yaml" \
        -f "$ROOT_DIR/terraform/rootfs/home/cloud-compose/scribe-runtime.compose.yaml" \
        --profile init \
        config --format json
  )"
  [[ "$(jq -c '.networks.default.ipam.config' <<<"$config")" == \
    "$(jq -c '.networks.default.ipam.config' <<<"$merged_config")" ]] ||
    fail "the rollback compatibility overlay drifted from docker-compose.yaml"

  SCRIBE_COMPOSE_SUBNET="$subnet" \
    SCRIBE_COMPOSE_GATEWAY="$gateway" \
    SCRIBE_COMPOSE_IP_RANGE="$dynamic_range" \
    SCRIBE_TRAEFIK_IP="$traefik" \
    SERVER_TRUSTED_PROXY_CIDRS="${traefik}/32" \
    bash scripts/validate-compose-runtime.sh >/dev/null
}

render_and_assert 172.30.0.0/24 172.30.0.1 172.30.0.128/25 172.30.0.2
render_and_assert 172.31.10.0/25 172.31.10.1 172.31.10.64/26 172.31.10.2
render_and_assert 172.31.20.0/26 172.31.20.1 172.31.20.32/27 172.31.20.2
render_and_assert 172.31.30.0/27 172.31.30.1 172.31.30.16/28 172.31.30.2
render_and_assert 172.31.40.0/28 172.31.40.1 172.31.40.8/29 172.31.40.2

if SCRIBE_COMPOSE_SUBNET=172.31.40.0/28 \
  SCRIBE_COMPOSE_GATEWAY=172.31.40.1 \
  SCRIBE_COMPOSE_IP_RANGE=172.31.40.0/29 \
  SCRIBE_TRAEFIK_IP=172.31.40.2 \
  bash scripts/validate-compose-runtime.sh >"$TEST_DIR/overlap.out" 2>&1; then
  fail "the preflight accepted a dynamic pool containing Traefik"
fi
if SCRIBE_COMPOSE_SUBNET=172.31.40.0/28 \
  SCRIBE_COMPOSE_GATEWAY=172.31.40.8 \
  SCRIBE_COMPOSE_IP_RANGE=172.31.40.8/29 \
  SCRIBE_TRAEFIK_IP=172.31.40.2 \
  bash scripts/validate-compose-runtime.sh >"$TEST_DIR/gateway.out" 2>&1; then
  fail "the preflight accepted a gateway inside the dynamic pool"
fi
if SCRIBE_COMPOSE_SUBNET=172.31.40.0/28 \
  SCRIBE_COMPOSE_GATEWAY=172.31.40.1 \
  SCRIBE_COMPOSE_IP_RANGE=172.31.40.8/29 \
  SCRIBE_TRAEFIK_IP=172.31.40.1 \
  bash scripts/validate-compose-runtime.sh >"$TEST_DIR/proxy-gateway.out" 2>&1; then
  fail "the preflight accepted Traefik on the bridge gateway"
fi

echo "Compose reserves Traefik outside a capacity-checked dynamic IPAM range."
