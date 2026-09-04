#!/usr/bin/env bash

set -euo pipefail

readonly AUDIT_ATTEMPTS=3
readonly AUDIT_RETRY_DELAY_SECONDS=5
readonly AUDIT_FETCH_TIMEOUT_MS=60000

if [ "$#" -ne 1 ] || [ ! -f "$1/package-lock.json" ]; then
  echo "Usage: $0 PACKAGE_DIRECTORY" >&2
  exit 2
fi

package_directory="$1"
npm_bin="${NPM_AUDIT_BIN:-npm}"

for ((attempt = 1; attempt <= AUDIT_ATTEMPTS; attempt++)); do
  if "$npm_bin" --prefix "$package_directory" audit \
    --audit-level=moderate \
    --fetch-retries=0 \
    --fetch-timeout="$AUDIT_FETCH_TIMEOUT_MS"; then
    exit 0
  else
    status=$?
  fi
  if [ "$attempt" -eq "$AUDIT_ATTEMPTS" ]; then
    exit "$status"
  fi
  echo "npm audit for ${package_directory} failed on attempt ${attempt}/${AUDIT_ATTEMPTS}; retrying." >&2
  sleep "$AUDIT_RETRY_DELAY_SECONDS"
done

exit 1
