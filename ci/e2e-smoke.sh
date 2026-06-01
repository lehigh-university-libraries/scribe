#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_TEST_IMAGE="${GO_TEST_IMAGE:-golang:1.26-alpine@sha256:91eda9776261207ea25fd06b5b7fed8d397dd2c0a283e77f2ab6e91bfa71079d}"

cd "${ROOT_DIR}"

if ! command -v docker >/dev/null 2>&1; then
  echo "Error: docker is required to run the e2e smoke test." >&2
  exit 127
fi

test -f .env || cp sample.env .env
bash generate-secrets.sh
docker compose up -d mariadb

MARIADB_ID="$(docker compose ps -q mariadb | head -1)"
if [ -z "${MARIADB_ID}" ]; then
  echo "Error: mariadb container is not running." >&2
  exit 1
fi

NETWORK="$(docker inspect "${MARIADB_ID}" --format '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' | awk '{print $1}')"
if [ -z "${NETWORK}" ]; then
  echo "Error: could not detect the docker compose network." >&2
  exit 1
fi

DB_USER="${MARIADB_USER:-scribe}"
DB_NAME="${MARIADB_DATABASE:-scribe}"
if [ -f "./secrets/mariadb_password" ]; then
  DB_PASSWORD="$(tr -d '\n' < ./secrets/mariadb_password)"
else
  DB_PASSWORD="scribe"
fi

docker run --rm \
  --network "${NETWORK}" \
  --mount "type=bind,src=${ROOT_DIR},dst=/app" \
  -e "TEST_DSN=${DB_USER}:${DB_PASSWORD}@tcp(mariadb:3306)/${DB_NAME}?parseTime=true" \
  -w /app \
  "${GO_TEST_IMAGE}" \
  sh -lc '
    apk add --no-cache build-base >/dev/null
    go test -v ./internal/server -run "TestManifestIngestLoadsHOCRAnnotations|TestAnnotationPageRevisionSaveSemantics" -count=1
  '
