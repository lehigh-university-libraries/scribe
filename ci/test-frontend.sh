#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FRONTEND_TEST_IMAGE="${FRONTEND_TEST_IMAGE:-node:24-alpine@sha256:d1b3b4da11eefd5941e7f0b9cf17783fc99d9c6fc34884a665f40a06dbdfc94f}"

if ! command -v docker >/dev/null 2>&1; then
  echo "Error: docker is required to run frontend tests." >&2
  exit 127
fi

run_tests='
    npm ci
    npm test
    npm run build
  '

if docker run --rm \
  --mount "type=bind,src=${ROOT_DIR},dst=/app" \
  -w /app/web \
  "$FRONTEND_TEST_IMAGE" \
  sh -c 'test -f package.json' >/dev/null 2>&1; then
  docker run --rm \
    --mount "type=bind,src=${ROOT_DIR},dst=/app" \
    --mount "type=volume,dst=/app/web/node_modules" \
    --mount "type=volume,dst=/app/web/dist" \
    -e CI=true \
    -w /app/web \
    "$FRONTEND_TEST_IMAGE" \
    sh -lc "$run_tests"
  exit 0
fi

container_id="$(
  docker create \
    -e CI=true \
    -w /app/web \
    "$FRONTEND_TEST_IMAGE" \
    sh -lc "$run_tests"
)"
cleanup() {
  docker rm -f "$container_id" >/dev/null 2>&1 || true
}
trap cleanup EXIT

tar --exclude=.git --exclude=web/node_modules --exclude=web/dist -C "$ROOT_DIR" -cf - . | docker cp - "$container_id:/app"
docker start -a "$container_id"
