#!/usr/bin/env bash

set -euo pipefail

# If mariadb is running via docker compose, join its network and set TEST_DSN
# so integration tests run alongside unit tests. Otherwise they are skipped.
NETWORK_ARGS=""
DSN_ARGS=""
DB_USER="${MARIADB_USER:-scribe}"
DB_NAME="${MARIADB_DATABASE:-scribe}"

MARIADB_ID=$(docker compose ps -q mariadb 2>/dev/null | head -1)
if [ -n "$MARIADB_ID" ]; then
  NETWORK=$(docker inspect "$MARIADB_ID" \
    --format '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' \
    | awk '{print $1}')
  if [ -n "$NETWORK" ]; then
    echo "MariaDB detected — running integration tests on network: $NETWORK"
    NETWORK_ARGS="--network $NETWORK"
    if [ -f "./secrets/mariadb_password" ]; then
      DB_PASSWORD=$(tr -d '\n' < ./secrets/mariadb_password)
    else
      DB_PASSWORD="scribe"
    fi
    DSN_ARGS="-e TEST_DSN=${DB_USER}:${DB_PASSWORD}@tcp(mariadb:3306)/${DB_NAME}?parseTime=true"
  fi
fi

GO_TEST_IMAGE="golang:1.26-alpine@sha256:91eda9776261207ea25fd06b5b7fed8d397dd2c0a283e77f2ab6e91bfa71079d"
run_tests="
    export PATH=\"/usr/local/go/bin:\$PATH\"
    apk add --no-cache build-base >/dev/null
    go test -v -race ./...
  "

if docker run --rm -v "$PWD:/app" -w /app "$GO_TEST_IMAGE" sh -c 'test -f go.mod' >/dev/null 2>&1; then
  # shellcheck disable=SC2086
  docker run --rm \
    $NETWORK_ARGS \
    $DSN_ARGS \
    -v "$PWD:/app" \
    -w /app \
    "$GO_TEST_IMAGE" \
    sh -lc "$run_tests"
  exit 0
fi

# shellcheck disable=SC2086
container_id="$(
  docker create \
    $NETWORK_ARGS \
    $DSN_ARGS \
    -w /app \
    "$GO_TEST_IMAGE" \
    sh -lc "$run_tests"
)"
cleanup() {
  docker rm -f "$container_id" >/dev/null 2>&1 || true
}
trap cleanup EXIT

tar --exclude=.git --exclude=web/node_modules --exclude=web/dist -C "$PWD" -cf - . | docker cp - "$container_id:/app"
docker start -a "$container_id"
