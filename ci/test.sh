#!/usr/bin/env bash

set -euo pipefail

# If mariadb is running via docker compose, join its network and set TEST_DSN
# so integration tests run alongside unit tests. Otherwise they are skipped.
NETWORK_ARGS=""
DSN_ARGS=""

MARIADB_ID=$(docker compose ps -q mariadb 2>/dev/null | head -1)
if [ -n "$MARIADB_ID" ]; then
  NETWORK=$(docker inspect "$MARIADB_ID" \
    --format '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' \
    | awk '{print $1}')
  if [ -n "$NETWORK" ]; then
    echo "MariaDB detected — running integration tests on network: $NETWORK"
    NETWORK_ARGS="--network $NETWORK"
    DSN_ARGS="-e TEST_DSN=scribe:scribe@tcp(mariadb:3306)/scribe?parseTime=true"
  fi
fi

# shellcheck disable=SC2086
docker run --rm \
  $NETWORK_ARGS \
  $DSN_ARGS \
  -v "$PWD:/app" \
  -w /app \
  golang:1.24-alpine \
  sh -lc '
    export PATH="/usr/local/go/bin:$PATH"
    apk add --no-cache build-base tesseract-ocr-dev leptonica-dev >/dev/null
    go test -v -race ./...
  '
