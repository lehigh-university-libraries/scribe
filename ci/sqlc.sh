#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly EXPECTED_SQLC_VERSION="v1.31.1"
SQLC_BIN="${SQLC_BIN:-}"
if [ -z "${SQLC_BIN}" ]; then
  if [ -x "${ROOT_DIR}/.tools/bin/sqlc" ]; then
    SQLC_BIN="${ROOT_DIR}/.tools/bin/sqlc"
  elif command -v sqlc >/dev/null 2>&1; then
    SQLC_BIN="$(command -v sqlc)"
  else
    echo "Error: sqlc is required. Run 'make install-tools'." >&2
    exit 127
  fi
fi

installed_sqlc_version="$("${SQLC_BIN}" version 2>/dev/null || true)"
if [ "$installed_sqlc_version" != "$EXPECTED_SQLC_VERSION" ]; then
  echo "Error: sqlc ${EXPECTED_SQLC_VERSION} is required; found '${installed_sqlc_version:-unknown}'. Run 'make install-codegen-tools'." >&2
  exit 127
fi

(cd "${ROOT_DIR}/sqlc" && "${SQLC_BIN}" generate)
