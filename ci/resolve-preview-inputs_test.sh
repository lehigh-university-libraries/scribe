#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

run_preview_unit_tests() {
  if command -v go >/dev/null 2>&1 &&
    [ "$(go env GOVERSION)" = "go$(tr -d '\n' <"$ROOT_DIR/.go-version")" ]; then
    (cd "$ROOT_DIR" && go test ./internal/deployer -run '^TestResolvePreview|^TestPreviewInputs')
    return
  fi

  docker run --rm --network none --read-only \
    --tmpfs /tmp:rw,noexec,nosuid,size=256m \
    -e GOCACHE=/tmp/go-build \
    -e GOMODCACHE=/tmp/go-mod \
    -v "$ROOT_DIR:/app:ro" \
    -w /app \
    golang:1.26.6-alpine@sha256:af8d6740070b8906d12eae1c3e3ea0957fb63f492051ea05e354c38ef9fe88df \
    go test ./internal/deployer -run '^TestResolvePreview|^TestPreviewInputs'
}

run_preview_unit_tests

echo "Typed preview input resolution tests passed."
