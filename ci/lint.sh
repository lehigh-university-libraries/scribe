#!/usr/bin/env bash

set -euo pipefail

IMAGE="${IMAGE:-ghcr.io/lehigh-university-libraries/hocredit:main}"
GOLANGCI_IMAGE="${GOLANGCI_IMAGE:-golangci/golangci-lint:v2.10.1-alpine}"

if command -v shellcheck >/dev/null 2>&1; then
  echo "Running ShellCheck..."
  shopt -s globstar nullglob
  shell_scripts=(**/*.sh)
  if ((${#shell_scripts[@]} > 0)); then
    shellcheck "${shell_scripts[@]}"
  fi
fi

echo "Installing golangci-lint"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT
docker run --rm \
  -v "$TMP_DIR:/out" \
  --entrypoint /bin/sh \
  "$GOLANGCI_IMAGE" \
  -c "cp \$(command -v golangci-lint) /out/golangci-lint && chmod +x /out/golangci-lint"

echo "Linting Go code..."
docker run --rm \
  --entrypoint /bin/bash \
  -v "$TMP_DIR/golangci-lint:/usr/local/bin/golangci-lint:ro" \
  -v "$(pwd):/app" \
  -w /app \
  "$IMAGE" \
  -c "/usr/local/bin/golangci-lint run"
