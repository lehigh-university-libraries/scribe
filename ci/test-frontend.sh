#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FRONTEND_TEST_IMAGE="${FRONTEND_TEST_IMAGE:-node:24.22.1-alpine@sha256:06d7adf3e0ef61a13f1d28682726366f97a15aa1888c934d3ecec86c33918b53}"

if ! command -v docker >/dev/null 2>&1; then
  echo "Error: docker is required to run frontend tests." >&2
  exit 127
fi

run_tests="
    set -eu
    cd /app/web
    npm ci --ignore-scripts --no-audit --progress=false
    npm test
    npm run build
    cd /app/mirador-scribe
    npm ci --ignore-scripts --no-audit --progress=false
    npm test
    npm run build
  "

container_id="$(
  docker create \
    -e CI=true \
    -w /app \
    "$FRONTEND_TEST_IMAGE" \
    sh -lc "$run_tests"
)"
cleanup() {
  docker rm -f "$container_id" >/dev/null 2>&1 || true
}
trap cleanup EXIT

tar \
  --exclude='web/node_modules*' \
  --exclude=web/dist \
  --exclude='mirador-scribe/node_modules*' \
  --exclude=mirador-scribe/dist \
  -C "$ROOT_DIR" -cf - web mirador-scribe | docker cp - "$container_id:/app"
docker start -a "$container_id"
