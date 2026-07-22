#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

# If mariadb is running via docker compose, join its network and set TEST_DSN
# so integration tests run alongside unit tests. Otherwise they are skipped.
NETWORK_ARGS=()
DSN_ARGS=()
DB_USER="${MARIADB_USER:-scribe}"
DB_NAME="${MARIADB_DATABASE:-scribe}"

MARIADB_ID=$(docker compose ps -q mariadb 2>/dev/null | head -1)
if [ -n "$MARIADB_ID" ]; then
  NETWORK=$(docker inspect "$MARIADB_ID" \
    --format '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' \
    | awk '{print $1}')
  if [ -n "$NETWORK" ]; then
    echo "MariaDB detected — running integration tests on network: $NETWORK"
    NETWORK_ARGS=(--network "${NETWORK}")
    if [ -f "./secrets/mariadb_password" ]; then
      DB_PASSWORD=$(tr -d '\n' < ./secrets/mariadb_password)
    else
      DB_PASSWORD="scribe"
    fi
    DSN_ARGS=(-e "TEST_DSN=${DB_USER}:${DB_PASSWORD}@tcp(mariadb:3306)/${DB_NAME}?parseTime=true")
  fi
fi

GO_TEST_IMAGE="golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2"
run_tests="
    export PATH=\"/usr/local/go/bin:\$PATH\"
    apk add --no-cache build-base libxml2-utils=2.13.9-r2 >/dev/null
    ./ci/export-schema-check.sh
    # DB-backed packages share the one isolated schema created by make ci.
    # Serialize packages so their global quota/outbox assertions cannot consume
    # one another's rows; tests within each package still retain normal Go
    # concurrency, including explicit race/admission scenarios. The persistent
    # build cache also contains test-result entries, so force every test to run
    # against this invocation's database rather than accepting a cached pass.
    go test -p=1 -count=1 -v -race ./...
  "

container_id="$(
  docker create \
    "${NETWORK_ARGS[@]}" \
    "${DSN_ARGS[@]}" \
    --mount "type=volume,src=scribe-go-test-build-cache-v1,dst=/root/.cache/go-build" \
    --mount "type=volume,src=scribe-go-test-mod-cache-v1,dst=/go/pkg/mod" \
    -w /app \
    "$GO_TEST_IMAGE" \
    sh -lc "$run_tests"
)"
cleanup() {
  docker rm -f "$container_id" >/dev/null 2>&1 || true
}
trap cleanup EXIT

tar \
  --exclude=.env \
  --exclude=.git \
  --exclude=.tools \
  --exclude='gha-creds-*.json' \
  --exclude=secrets \
  --exclude=terraform/.terraform \
  --exclude=site \
  --exclude='web/node_modules*' \
  --exclude=web/dist \
  --exclude='mirador-scribe/node_modules*' \
  --exclude=mirador-scribe/dist \
  -C "${ROOT_DIR}" -cf - . | docker cp - "$container_id:/app"
docker start -a "$container_id"
