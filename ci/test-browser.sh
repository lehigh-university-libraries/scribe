#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Keep this image version identical to web's @playwright/test dependency.
# renovate: datasource=docker depName=mcr.microsoft.com/playwright
PLAYWRIGHT_TEST_IMAGE="${PLAYWRIGHT_TEST_IMAGE:-mcr.microsoft.com/playwright:v1.62.0-noble@sha256:baed2032d533817f3dbe6425de795788430ba345e819a1201337009ba17c9d07}"
GO_TEST_IMAGE="${GO_TEST_IMAGE:-golang:1.26.6-alpine@sha256:af8d6740070b8906d12eae1c3e3ea0957fb63f492051ea05e354c38ef9fe88df}"

if ! command -v docker >/dev/null 2>&1; then
  echo "Error: Docker is required to run Chromium acceptance tests." >&2
  exit 127
fi

network_args=()
browser_env=()
backend_container_id=""
container_id=""
backend_ready_timeout="${SCRIBE_BROWSER_BACKEND_READY_TIMEOUT_SECONDS:-600}"
if [[ ! "${backend_ready_timeout}" =~ ^[0-9]+$ ]] || [ "${backend_ready_timeout}" -lt 30 ] || [ "${backend_ready_timeout}" -gt 900 ]; then
  echo "Error: SCRIBE_BROWSER_BACKEND_READY_TIMEOUT_SECONDS must be an integer from 30 through 900." >&2
  exit 2
fi
# Invoked by the EXIT trap below.
# shellcheck disable=SC2329
cleanup() {
  if [ -n "${container_id}" ]; then
    docker rm -f "${container_id}" >/dev/null 2>&1 || true
  fi
  if [ -n "${backend_container_id}" ]; then
    docker stop --time 15 "${backend_container_id}" >/dev/null 2>&1 || true
    docker rm -f "${backend_container_id}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

db_user="${MARIADB_USER:-scribe}"
db_name="${MARIADB_DATABASE:-scribe}"
mariadb_id="${SCRIBE_BROWSER_MARIADB_CONTAINER_ID:-}"
if [ -z "${mariadb_id}" ]; then
  mariadb_id="$(cd "${ROOT_DIR}" && docker compose ps -q mariadb 2>/dev/null | head -1)"
fi
if [ -n "${mariadb_id}" ]; then
  network="$(docker inspect "${mariadb_id}" \
    --format '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' \
    | awk '{print $1}')"
  if [ -z "${network}" ]; then
    echo "Error: could not resolve the MariaDB container network." >&2
    exit 1
  fi
  db_password="scribe"
  if [ -f "${ROOT_DIR}/secrets/mariadb_password" ]; then
    db_password="$(tr -d '\n' < "${ROOT_DIR}/secrets/mariadb_password")"
  fi
  # Always bind the fixture to the MariaDB container resolved above. Inheriting
  # an ambient TEST_DSN could make this destructive acceptance fixture mutate a
  # developer's unrelated database while the rest of make ci uses Compose.
  test_dsn="${db_user}:${db_password}@tcp(mariadb:3306)/${db_name}?parseTime=true"
  network_args=(--network "${network}")
  browser_env=(
    -e SCRIBE_DEV_BACKEND_ORIGIN=http://scribe-browser-backend:8080
    -e VITE_SCRIBE_BROWSER_BACKEND=true
  )

  backend_command="
    export PATH=\"/usr/local/go/bin:\$PATH\"
    apk add --no-cache build-base >/dev/null
    go run ./cmd/browser-test-server
  "
  backend_container_id="$(
    docker create \
      --init \
      --network "${network}" \
      --network-alias scribe-browser-backend \
      --mount "type=volume,src=scribe-go-test-build-cache-v1,dst=/root/.cache/go-build" \
      --mount "type=volume,src=scribe-go-test-mod-cache-v1,dst=/go/pkg/mod" \
      -e "TEST_DSN=${test_dsn}" \
      -w /app \
      "${GO_TEST_IMAGE}" \
      sh -lc "${backend_command}"
  )"
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
    -C "${ROOT_DIR}" -cf - . | docker cp - "${backend_container_id}:/app"
  docker start "${backend_container_id}" >/dev/null
  backend_ready=false
  for ((attempt = 0; attempt < backend_ready_timeout; attempt++)); do
    if docker exec "${backend_container_id}" wget -q -O - http://127.0.0.1:8080/readyz >/dev/null 2>&1; then
      backend_ready=true
      break
    fi
    if [ "$(docker inspect -f '{{.State.Running}}' "${backend_container_id}")" != "true" ]; then
      break
    fi
    sleep 1
  done
  if [ "${backend_ready}" != "true" ]; then
    echo "Error: browser Connect fixture did not become ready." >&2
    docker logs "${backend_container_id}" >&2 || true
    exit 1
  fi
  fixture_item_image_id="$(docker exec "${backend_container_id}" wget -q -O - http://127.0.0.1:8080/__browser-fixture/item-image-id)"
  if [[ ! "${fixture_item_image_id}" =~ ^[1-9][0-9]*$ ]]; then
    echo "Error: browser Connect fixture returned an invalid item image ID." >&2
    exit 1
  fi
  browser_env+=(-e "VITE_SCRIBE_BROWSER_ITEM_IMAGE_ID=${fixture_item_image_id}")
elif [ "${SCRIBE_REQUIRE_BROWSER_BACKEND:-false}" = "true" ]; then
  echo "Error: the required MariaDB-backed browser fixture is unavailable." >&2
  exit 1
else
  echo "MariaDB is not running; using the in-browser persistence fixture."
fi

run_tests="
  cd /app/web
  npm ci --ignore-scripts --no-audit --progress=false
  npm run test:browser:types
  npm run test:browser
"

container_id="$(
  docker create \
    --init \
    --ipc=host \
    "${network_args[@]}" \
    --mount "type=volume,src=scribe-playwright-npm-cache-v1,dst=/root/.npm" \
    -e CI=true \
    -e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
    "${browser_env[@]}" \
    -w /app \
    "${PLAYWRIGHT_TEST_IMAGE}" \
    sh -lc "${run_tests}"
)"
tar \
  --exclude='web/node_modules*' \
  --exclude=web/dist \
  --exclude=web/test-results \
  --exclude=web/playwright-report \
  --exclude='mirador-scribe/node_modules*' \
  --exclude=mirador-scribe/dist \
  -C "${ROOT_DIR}" -cf - web mirador-scribe | docker cp - "${container_id}:/app"
set +e
docker start -a "${container_id}"
test_status=$?
set -e
if [ "${test_status}" -ne 0 ]; then
  mkdir -p "${ROOT_DIR}/web/test-results"
  if docker cp "${container_id}:/app/web/test-results/." "${ROOT_DIR}/web/test-results"; then
    echo "Playwright failure artifacts: ${ROOT_DIR}/web/test-results" >&2
  fi
fi
exit "${test_status}"
