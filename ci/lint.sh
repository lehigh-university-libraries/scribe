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

if docker run --rm -v "$PWD:/app" -w /app "${GOLANGCI_IMAGE:?GOLANGCI_IMAGE is required}" sh -c 'test -f go.mod' >/dev/null 2>&1; then
  docker run --rm \
    -v "$PWD:/app" \
    -w /app \
    "${GOLANGCI_IMAGE:?GOLANGCI_IMAGE is required}" \
    golangci-lint run
  exit 0
fi

container_id="$(
  docker create \
    -w /app \
    "${GOLANGCI_IMAGE:?GOLANGCI_IMAGE is required}" \
    golangci-lint run
)"
cleanup() {
  docker rm -f "$container_id" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker cp "$PWD/." "$container_id:/app"
docker start -a "$container_id"
