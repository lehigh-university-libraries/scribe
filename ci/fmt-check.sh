#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

# renovate: datasource=docker depName=golang
GO_IMAGE="${GO_IMAGE:-golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2}"

go_files=()
while IFS= read -r go_file; do
  go_files+=("${go_file}")
done < <(rg --files -g '*.go')
if ((${#go_files[@]} == 0)); then
  exit 0
fi

gofmt_bin="$(command -v gofmt || true)"
if [ -z "${gofmt_bin}" ] && [ -x /usr/local/go/bin/gofmt ]; then
  gofmt_bin=/usr/local/go/bin/gofmt
fi

if [ -n "${gofmt_bin}" ]; then
  unformatted="$("${gofmt_bin}" -l "${go_files[@]}")"
elif command -v docker >/dev/null 2>&1; then
  if docker run --rm \
    --volume "${ROOT_DIR}:/workspace:ro" \
    --workdir /workspace \
    --entrypoint sh \
    "${GO_IMAGE}" \
    -c 'test -f go.mod' >/dev/null 2>&1; then
    unformatted="$(docker run --rm \
      --volume "${ROOT_DIR}:/workspace:ro" \
      --workdir /workspace \
      "${GO_IMAGE}" \
      gofmt -l "${go_files[@]}")"
  else
    container_id="$(docker create \
      --workdir /workspace \
      "${GO_IMAGE}" \
      gofmt -l "${go_files[@]}")"
    cleanup() {
      docker rm -f "${container_id}" >/dev/null 2>&1 || true
    }
    trap cleanup EXIT
    tar -C "${ROOT_DIR}" -cf - "${go_files[@]}" |
      docker cp - "${container_id}:/workspace"
    unformatted="$(docker start -a "${container_id}")"
    cleanup
    trap - EXIT
  fi
else
  echo "Error: gofmt or Docker is required." >&2
  exit 127
fi
if [ -n "${unformatted}" ]; then
  echo "The following Go files are not gofmt-formatted:" >&2
  printf '%s\n' "${unformatted}" >&2
  exit 1
fi
