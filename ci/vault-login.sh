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
require_env "VAULT_ADMIN_TOKEN"
require_env "VAULT_ID_TOKEN"
require_env "VAULT_ROLE"
require_env "GITHUB_ENV"

response_file="$(mktemp)"
status_code_file="$(mktemp)"
trap 'rm -f "$response_file" "$status_code_file"' EXIT

payload="$(jq -cn --arg role "${VAULT_ROLE}" --arg jwt "${VAULT_ID_TOKEN}" '{role: $role, jwt: $jwt}')"
max_attempts="${VAULT_LOGIN_MAX_ATTEMPTS:-10}"
attempt=1
delay_seconds="${VAULT_LOGIN_INITIAL_DELAY_SECONDS:-1}"

while true; do
  : > "$response_file"
  : > "$status_code_file"

  if curl -sS --connect-timeout 5 --max-time 30 -o "$response_file" -w '%{http_code}' \
    -X POST \
    -H "Content-Type: application/json" \
    -H "X-Admin-Token: ${VAULT_ADMIN_TOKEN}" \
    --data "$payload" \
    "${VAULT_ADDR%/}/v1/auth/google-jwt/login" >"$status_code_file"; then
    curl_exit=0
  else
    curl_exit=$?
  fi

  status_code="$(tr -d '\n' < "$status_code_file")"
  if [ "$curl_exit" -eq 0 ] && [ -n "$status_code" ] && [ "$status_code" -ge 200 ] && [ "$status_code" -lt 300 ]; then
    break
  fi

  retryable=false
  if [ "$curl_exit" -ne 0 ]; then
    retryable=true
  elif [ -n "$status_code" ] && [ "$status_code" -ge 500 ] && [ "$status_code" -lt 600 ]; then
    retryable=true
  fi

  if [ "$attempt" -ge "$max_attempts" ] || [ "$retryable" != "true" ]; then
    if [ "$curl_exit" -ne 0 ]; then
      echo "Vault login request failed for role ${VAULT_ROLE} at ${VAULT_ADDR} after ${attempt} attempt(s) (curl exit ${curl_exit})." >&2
    else
      echo "Vault login failed (HTTP ${status_code}) for role ${VAULT_ROLE} at ${VAULT_ADDR} after ${attempt} attempt(s)." >&2
    fi
    echo "Common causes: secrets.GSA is missing from vault_ci_service_account_emails, the owning workspace Vault bootstrap has not been re-applied since that change, or the Vault URL/audience no longer matches the configured role." >&2
    if [ -s "$response_file" ]; then
      echo "Vault response body withheld because it may contain sensitive authentication material." >&2
      if jq -e --arg role "$VAULT_ROLE" '.errors[]? | contains("role \"" + $role + "\" could not be found")' "$response_file" >/dev/null 2>&1; then
        echo "Vault is reachable, but the JWT auth role ${VAULT_ROLE} does not exist yet." >&2
        echo "Apply the owning Vault workspace with a tfvars entry that includes the GitHub Actions service account from secrets.GSA under vault_ci_service_account_emails." >&2
        echo "For preview/dev workflows, re-apply workspace dev. For production workflows, re-apply workspace prod." >&2
      elif jq -e '.errors[]? | contains("non-admin hitting protected route")' "$response_file" >/dev/null 2>&1; then
        echo "Vault rejected the X-Admin-Token before evaluating role ${VAULT_ROLE}." >&2
        echo "This usually means the Vault proxy admin allow-list was not refreshed after adding or changing secrets.GSA." >&2
        echo "Re-apply the owning Vault workspace so module.vault picks up vault_ci_service_account_emails and updates the proxy config." >&2
        echo "For preview/dev workflows, re-apply workspace dev. For production workflows, re-apply workspace prod." >&2
      fi
    fi
    exit 1
  fi

  if [ "$curl_exit" -ne 0 ]; then
    echo "Vault login attempt ${attempt}/${max_attempts} hit a transport error (curl exit ${curl_exit}); retrying in ${delay_seconds}s." >&2
  else
    echo "Vault login attempt ${attempt}/${max_attempts} returned HTTP ${status_code}; retrying in ${delay_seconds}s." >&2
  fi
  sleep "$delay_seconds"
  attempt=$((attempt + 1))
  delay_seconds=$((delay_seconds * 2))
done

vault_token="$(jq -r '.auth.client_token // empty' "$response_file")"
if [ -z "$vault_token" ]; then
  echo "Vault response body withheld because it may contain sensitive authentication material." >&2
  echo "Vault login response did not include auth.client_token for role ${VAULT_ROLE}" >&2
  exit 1
fi

echo "::add-mask::${vault_token}"

{
  echo "VAULT_ADDR=${VAULT_ADDR}"
  echo "VAULT_TOKEN=${vault_token}"
} >> "$GITHUB_ENV"
