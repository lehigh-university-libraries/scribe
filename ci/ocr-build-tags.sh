#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_TEST_IMAGE="${GO_TEST_IMAGE:-golang:1.26-alpine@sha256:91eda9776261207ea25fd06b5b7fed8d397dd2c0a283e77f2ab6e91bfa71079d}"

if ! command -v docker >/dev/null 2>&1; then
  echo "Error: docker is required to test OCR build tags." >&2
  exit 127
fi

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
    CGO_ENABLED=1 /usr/local/go/bin/go build -tags localocr ./cmd/segmentor
  '
)"
cleanup() {
  docker rm -f "$container_id" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker cp "${ROOT_DIR}/." "${container_id}:/app"
docker start -a "$container_id"
