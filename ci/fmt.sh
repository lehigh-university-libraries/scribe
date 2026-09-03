#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

# renovate: datasource=docker depName=golang
GO_IMAGE="${GO_IMAGE:-golang:1.27.1-alpine@sha256:cf6fca6641884b8433441b2b0652976f975e1d0fdd26d177eaaf8596087f3125}"

echo "Formatting Go code..."

files=()
while IFS= read -r file; do
  files+=("${file}")
done < <(
  {
    git diff --name-only --diff-filter=ACMR -- '*.go'
    git diff --cached --name-only --diff-filter=ACMR -- '*.go'
    git ls-files --others --exclude-standard -- '*.go'
  } | sort -u
)

if ((${#files[@]} == 0)); then
  echo "No changed Go files to format"
  exit 0
fi

gofmt_bin="$(command -v gofmt || true)"
if [ -z "${gofmt_bin}" ] && [ -x /usr/local/go/bin/gofmt ]; then
  gofmt_bin=/usr/local/go/bin/gofmt
fi

if [ -n "${gofmt_bin}" ]; then
  "${gofmt_bin}" -w "${files[@]}"
elif command -v docker >/dev/null 2>&1; then
  if docker run --rm \
    --volume "${ROOT_DIR}:/workspace" \
    --workdir /workspace \
    --entrypoint sh \
    "${GO_IMAGE}" \
    -c 'test -f go.mod' >/dev/null 2>&1; then
    docker run --rm \
      --user "$(id -u):$(id -g)" \
      --volume "${ROOT_DIR}:/workspace" \
      --workdir /workspace \
      "${GO_IMAGE}" \
      gofmt -w "${files[@]}"
  else
    container_id="$(docker create \
      --workdir /workspace \
      "${GO_IMAGE}" \
      gofmt -w "${files[@]}")"
    cleanup() {
      docker rm -f "${container_id}" >/dev/null 2>&1 || true
    }
    trap cleanup EXIT
    tar -C "${ROOT_DIR}" -cf - "${files[@]}" |
      docker cp - "${container_id}:/workspace"
    docker start -a "${container_id}"
    for file in "${files[@]}"; do
      docker cp "${container_id}:/workspace/${file}" "${ROOT_DIR}/${file}"
    done
    cleanup
    trap - EXIT
  fi
else
  echo "Error: gofmt or Docker is required." >&2
  exit 127
fi
