#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_TEST_IMAGE="${GO_TEST_IMAGE:-golang:1.27.1-alpine@sha256:cf6fca6641884b8433441b2b0652976f975e1d0fdd26d177eaaf8596087f3125}"

if ! command -v docker >/dev/null 2>&1; then
  echo "Error: docker is required to test OCR build tags." >&2
  exit 127
fi

bash "${ROOT_DIR}/scripts/install-kraken-models_test.sh"

container_id="$(
  docker create \
    -w /app \
    "$GO_TEST_IMAGE" \
    sh -lc '
    set -eu
    apk add --no-cache build-base pkgconf tesseract-ocr-dev leptonica-dev >/dev/null
    /usr/local/go/bin/go test ./internal/worddetection ./internal/hocr ./internal/handlers
    /usr/local/go/bin/go test -tags remoteocr ./internal/worddetection ./internal/hocr ./internal/handlers
    /usr/local/go/bin/go test -tags localocr ./internal/worddetection ./internal/hocr ./internal/handlers
    CGO_ENABLED=0 /usr/local/go/bin/go build -tags remoteocr ./cmd/api ./cmd/worker
    CGO_ENABLED=0 GOOS=linux GOARCH=386 /usr/local/go/bin/go build -trimpath -tags remoteocr ./cmd/api ./cmd/worker
    CGO_ENABLED=1 /usr/local/go/bin/go build -tags localocr ./cmd/segmentor
  '
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
  -C "${ROOT_DIR}" -cf - . | docker cp - "${container_id}:/app"
docker start -a "$container_id"
