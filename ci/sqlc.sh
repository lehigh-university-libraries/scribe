#!/usr/bin/env bash

set -euo pipefail

if ! command -v sqlc >/dev/null 2>&1; then
  echo "sqlc is not installed; skipping sqlc generation"
  exit 0
fi

(cd sqlc && sqlc generate)
