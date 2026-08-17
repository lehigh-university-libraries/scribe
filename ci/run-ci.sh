#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CI_COMPOSE_PROJECT="${SCRIBE_CI_COMPOSE_PROJECT:-scribe-ci-$(date +%s)-$$}"
MAKE_COMMAND="${SCRIBE_MAKE_COMMAND:-make}"
DATABASE_STARTED=false

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

start_database() {
  DATABASE_STARTED=true
  run_make up-db
}

run_contracts_group() {
  run_make lint
  run_make generate-check
}

run_test_group() {
  run_make test-frontend
  start_database
  SCRIBE_REQUIRE_TEST_DB=true run_make test-backend
  cleanup_database
  run_make ocr-build-tags
}

run_browser_group() {
  start_database
  SCRIBE_REQUIRE_BROWSER_BACKEND=true run_make test-browser
  cleanup_database
}

run_recovery_group() {
  run_make backup-restore-smoke
  run_make verify-cloud-backups-test
  run_make cloud-snapshot-restore-drill-test
  run_make mariadb-backup-retention-test
  run_make preview-deployment-test
  run_make readiness-fixture-test
  run_make deployment-status-test
}

run_security_group() {
  run_make security
  run_make dependency-scan
}

run_infrastructure_group() {
  run_make terraform-check
  run_make terraform-state-normalizer-test
  run_make terraform-targeted-output-test
  run_make ops-tests
  run_make docs-build
}

run_all_groups() {
  run_contracts_group
  # The full local contract reuses one isolated database for browser and Go
  # integration checks. Hosted jobs invoke the same two groups independently.
  run_make test-frontend
  start_database
  SCRIBE_REQUIRE_BROWSER_BACKEND=true run_make test-browser
  SCRIBE_REQUIRE_TEST_DB=true run_make test-backend
  cleanup_database
  run_make ocr-build-tags
  run_recovery_group
  run_security_group
  run_infrastructure_group
}

group="${1:-all}"
case "$group" in
  all) run_all_groups ;;
  contracts) run_contracts_group ;;
  test) run_test_group ;;
  browser) run_browser_group ;;
  recovery) run_recovery_group ;;
  security) run_security_group ;;
  infrastructure) run_infrastructure_group ;;
  --list) printf '%s\n' contracts test browser recovery security infrastructure ;;
  *)
    echo "Usage: $0 [all|contracts|test|browser|recovery|security|infrastructure|--list]" >&2
    exit 2
    ;;
esac
