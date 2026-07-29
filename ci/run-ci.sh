#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CI_COMPOSE_PROJECT="${SCRIBE_CI_COMPOSE_PROJECT:-scribe-ci-$(date +%s)-$$}"
MAKE_COMMAND="${SCRIBE_MAKE_COMMAND:-make}"
DATABASE_STARTED=false

reserve_ci_compose_network() {
  local compose_network="${COMPOSE_PROJECT_NAME}_default"
  local compose_version
  local gateway
  local ip_range
  local octet
  local prefix
  local subnet
  local traefik_ip

  compose_version="$(docker compose version --short)"
  for prefix in 172.31 10.231 192.168; do
    for ((octet = 0; octet <= 255; octet++)); do
      subnet="${prefix}.${octet}.0/24"
      gateway="${prefix}.${octet}.1"
      traefik_ip="${prefix}.${octet}.2"
      ip_range="${prefix}.${octet}.128/25"
      if docker network create \
        --driver bridge \
        --subnet "$subnet" \
        --ip-range "$ip_range" \
        --gateway "$gateway" \
        --label "com.docker.compose.project=${COMPOSE_PROJECT_NAME}" \
        --label "com.docker.compose.network=default" \
        --label "com.docker.compose.version=${compose_version}" \
        "$compose_network" >/dev/null 2>&1; then
        export SCRIBE_COMPOSE_SUBNET="$subnet"
        export SCRIBE_COMPOSE_GATEWAY="$gateway"
        export SCRIBE_COMPOSE_IP_RANGE="$ip_range"
        export SCRIBE_TRAEFIK_IP="$traefik_ip"
        export SERVER_TRUSTED_PROXY_CIDRS="${traefik_ip}/32"
        echo "Reserved isolated CI Compose network ${compose_network} (${subnet})."
        return 0
      fi
    done
  done

  echo "Could not reserve an unused private /24 for ${compose_network}." >&2
  return 1
}

if [[ ! "${CI_COMPOSE_PROJECT}" =~ ^scribe-ci-[a-z0-9-]+$ ]]; then
  echo "SCRIBE_CI_COMPOSE_PROJECT must start with scribe-ci- and contain only lowercase letters, numbers, and hyphens." >&2
  exit 2
fi

export COMPOSE_PROJECT_NAME="${CI_COMPOSE_PROJECT}"
export COMPOSE_FILE="${ROOT_DIR}/docker-compose.yaml"
cd "${ROOT_DIR}"

run_make() {
  "${MAKE_COMMAND}" --no-print-directory "$@"
}

cleanup_database() {
  if [ "${DATABASE_STARTED}" != "true" ]; then
    return
  fi
  echo "Removing isolated CI database project ${COMPOSE_PROJECT_NAME}..."
  if ! docker compose down --volumes --remove-orphans; then
    echo "Warning: failed to remove isolated CI database project ${COMPOSE_PROJECT_NAME}." >&2
  fi
  DATABASE_STARTED=false
}
trap cleanup_database EXIT

run_make lint
run_make generate-check
run_make test-frontend

DATABASE_STARTED=true
reserve_ci_compose_network
run_make up-db
SCRIBE_REQUIRE_BROWSER_BACKEND=true run_make test-browser
SCRIBE_REQUIRE_TEST_DB=true run_make test-backend
cleanup_database

run_make backup-restore-smoke
run_make verify-cloud-backups-test
run_make cloud-snapshot-restore-drill-test
run_make mariadb-backup-retention-test
run_make preview-deployment-test
run_make readiness-fixture-test
run_make deployment-status-test
run_make ocr-build-tags
run_make security
run_make dependency-scan
run_make terraform-check
run_make terraform-state-normalizer-test
run_make terraform-targeted-output-test
run_make ops-security-contracts
run_make docs-build
