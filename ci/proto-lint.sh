#!/usr/bin/env bash

set -euo pipefail

if ! command -v buf >/dev/null 2>&1; then
  echo "buf is not installed; skipping proto lint"
  exit 0
fi

(cd proto && buf lint)
