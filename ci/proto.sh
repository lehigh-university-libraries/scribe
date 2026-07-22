#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly EXPECTED_BUF_VERSION="1.72.0"
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

installed_buf_version="$("${BUF_BIN}" --version 2>/dev/null || true)"
if [ "$installed_buf_version" != "$EXPECTED_BUF_VERSION" ]; then
  echo "Error: buf ${EXPECTED_BUF_VERSION} is required; found '${installed_buf_version:-unknown}'. Run 'make install-codegen-tools'." >&2
  exit 127
fi

# Generation consumes the reviewed commits in buf.lock. Dependency upgrades
# are explicit maintenance changes; CI must never rewrite the lock to whatever
# happens to be latest on the network.
(cd "${ROOT_DIR}/proto" && "${BUF_BIN}" build . && "${BUF_BIN}" generate)

GO_BIN="${GO_BIN:-}"
if [ -z "${GO_BIN}" ]; then
  if command -v go >/dev/null 2>&1; then
    GO_BIN="$(command -v go)"
  elif [ -x /usr/local/go/bin/go ]; then
    GO_BIN=/usr/local/go/bin/go
  else
    echo "Error: Go is required to finalize the OpenAPI contract. Run 'make install-tools'." >&2
    exit 127
  fi
fi
(cd "${ROOT_DIR}" && "${GO_BIN}" run ./cmd/openapi-postprocess "${ROOT_DIR}/docs/api/scribe.openapi.yaml")
