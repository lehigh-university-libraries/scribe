#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUF_BIN="${BUF_BIN:-}"
if [ -z "${BUF_BIN}" ]; then
  if [ -x "${ROOT_DIR}/.tools/bin/buf" ]; then
    BUF_BIN="${ROOT_DIR}/.tools/bin/buf"
  elif command -v buf >/dev/null 2>&1; then
    BUF_BIN="$(command -v buf)"
  else
    echo "Error: buf is required. Run 'make install-tools'." >&2
    exit 127
  fi
fi

(cd "${ROOT_DIR}/proto" && "${BUF_BIN}" lint)
