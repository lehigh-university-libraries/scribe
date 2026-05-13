#!/usr/bin/env bash

set -euo pipefail

if command -v shellcheck >/dev/null 2>&1; then
  echo "Running ShellCheck..."
  shopt -s globstar nullglob
  shell_scripts=(**/*.sh)
  if ((${#shell_scripts[@]} > 0)); then
    shellcheck "${shell_scripts[@]}"
  fi
fi

echo "Running golangci-lint..."
if command -v golangci-lint >/dev/null 2>&1; then
  golangci-lint run
  exit 0
fi

docker run --rm \
  -v "$PWD:/app" \
  -w /app \
  "${GOLANGCI_IMAGE:?GOLANGCI_IMAGE is required}" \
  golangci-lint run
