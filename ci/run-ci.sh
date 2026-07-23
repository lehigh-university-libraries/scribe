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

run_make toolchain-check
run_make lint
run_make generate-check
run_make test-frontend

DATABASE_STARTED=true
run_make up-db
SCRIBE_REQUIRE_BROWSER_BACKEND=true run_make test-browser
run_make test-backend
run_make e2e-smoke
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
