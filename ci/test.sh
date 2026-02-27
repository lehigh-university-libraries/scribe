#!/usr/bin/env bash

set -euo pipefail

docker run --rm \
  -v "$PWD:/app" \
  -w /app \
  golang:1.24-alpine \
  sh -lc '
    export PATH="/usr/local/go/bin:$PATH"
    apk add --no-cache build-base tesseract-ocr-dev leptonica-dev >/dev/null
    go test ./...
  '
