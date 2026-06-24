#!/usr/bin/env bash

set -euo pipefail

require_env() {
  local name="$1"
  if [ -z "${!name:-}" ]; then
    echo "Missing required environment variable: ${name}" >&2
    exit 1
  fi
}

require_env "VAULT_ADDR"
require_env "VAULT_TOKEN"

policy_file="${1:-terraform/policies/vault/ci.hcl}"
if [ ! -f "$policy_file" ]; then
  echo "Vault CI policy file not found: ${policy_file}" >&2
  exit 1
fi

response_file="$(mktemp)"
status_code_file="$(mktemp)"
trap 'rm -f "$response_file" "$status_code_file"' EXIT

payload="$(jq -n --rawfile policy "$policy_file" '{policy: $policy}')"
curl_headers=(
  -H "Content-Type: application/json"
  -H "X-Vault-Token: ${VAULT_TOKEN}"
)

if [ -n "${VAULT_ADMIN_TOKEN:-}" ]; then
  curl_headers+=(-H "X-Admin-Token: ${VAULT_ADMIN_TOKEN}")
fi

if curl -sS -o "$response_file" -w '%{http_code}' \
  -X PUT \
  "${curl_headers[@]}" \
  --data "$payload" \
  "${VAULT_ADDR%/}/v1/sys/policies/acl/ci" >"$status_code_file"; then
  curl_exit=0
else
  curl_exit=$?
fi

status_code="$(tr -d '\n' < "$status_code_file")"
if [ "$curl_exit" -ne 0 ] || [ -z "$status_code" ] || [ "$status_code" -lt 200 ] || [ "$status_code" -ge 300 ]; then
  if [ "$curl_exit" -ne 0 ]; then
    echo "Vault CI policy sync request failed (curl exit ${curl_exit})." >&2
  else
    echo "Vault CI policy sync failed with HTTP ${status_code}." >&2
  fi
  if [ -s "$response_file" ]; then
    cat "$response_file" >&2
  fi
  exit 1
fi

echo "Vault CI policy refreshed from ${policy_file}."
