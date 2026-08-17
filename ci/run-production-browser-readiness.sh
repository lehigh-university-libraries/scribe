#!/usr/bin/env bash

set -euo pipefail

readiness_binary="${SCRIBE_PRODUCTION_BROWSER_READINESS_BIN:-}"
if [[ -z "$readiness_binary" || "$readiness_binary" != /* \
  || "$readiness_binary" == *$'\n'* || "$readiness_binary" == *$'\r'* \
  || ! -f "$readiness_binary" || -L "$readiness_binary" \
  || ! -x "$readiness_binary" ]]; then
  echo "Production browser readiness launcher failed: SCRIBE_PRODUCTION_BROWSER_READINESS_BIN must identify an absolute executable regular file." >&2
  exit 2
fi

exec "$readiness_binary" "$@"
