#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"
COMPOSE_PROJECT_NAME="scribe-ci-mysql-$(date +%s)-$$"
export COMPOSE_PROJECT_NAME
COMPOSE_FILE="$ROOT_DIR/ci/mysql-compose.yaml"
export COMPOSE_FILE
cleanup() { docker compose down --volumes --remove-orphans >/dev/null; }
trap cleanup EXIT
docker compose up -d --wait --wait-timeout 180
MARIADB_PASSWORD=mysql-contract-password SCRIBE_REQUIRE_TEST_DB=true \
  SCRIBE_TEST_DB_PASSWORD=mysql-contract-password ./ci/test.sh
