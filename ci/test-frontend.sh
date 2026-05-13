#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FRONTEND_TEST_IMAGE="${FRONTEND_TEST_IMAGE:-node:24-alpine@sha256:d1b3b4da11eefd5941e7f0b9cf17783fc99d9c6fc34884a665f40a06dbdfc94f}"

if ! command -v docker >/dev/null 2>&1; then
  echo "Error: docker is required to run frontend tests." >&2
  exit 127
fi

docker run --rm \
  --mount "type=bind,src=${ROOT_DIR},dst=/app" \
  --mount "type=volume,dst=/app/web/node_modules" \
  --mount "type=volume,dst=/app/web/dist" \
  -e CI=true \
  -w /app/web \
  "$FRONTEND_TEST_IMAGE" \
  sh -lc '
    npm ci
    npm test
    npm run build
  '
