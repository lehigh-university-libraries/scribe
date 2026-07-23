#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck disable=SC1091 # ROOT_DIR is resolved and validated by the test invocation.
source "$ROOT_DIR/scripts/vault-retry.sh"

attempts=0
transient_login() {
  attempts=$((attempts + 1))
  [ "$attempts" -ge 3 ]
}

log_file="$(mktemp)"
trap 'rm -f "$log_file"' EXIT
VAULT_RETRY_ATTEMPTS=5 VAULT_RETRY_INITIAL_DELAY_SECONDS=0 VAULT_RETRY_MAX_DELAY_SECONDS=0 \
  vault_retry "Vault GCP login" transient_login 2>"$log_file"
[ "$attempts" -eq 3 ] || { echo "Vault retry attempts = $attempts, want 3" >&2; exit 1; }
[ "$(rg -c 'failed; retrying' "$log_file")" -eq 2 ] || { echo "Vault retry did not record two transient failures" >&2; exit 1; }
if rg -q '403|404|500|503|token|response' "$log_file"; then
  echo "Vault retry log exposed response details" >&2
  exit 1
fi

attempts=0
always_unavailable() {
  attempts=$((attempts + 1))
  return 1
}
if VAULT_RETRY_ATTEMPTS=4 VAULT_RETRY_INITIAL_DELAY_SECONDS=0 VAULT_RETRY_MAX_DELAY_SECONDS=0 \
  vault_retry "Vault database app secret read" always_unavailable 2>"$log_file"; then
  echo "Vault retry accepted permanent failure" >&2
  exit 1
fi
[ "$attempts" -eq 4 ] || { echo "Vault exhausted attempts = $attempts, want 4" >&2; exit 1; }

echo "Vault bootstrap retry and redaction contracts passed."
