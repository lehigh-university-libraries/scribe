#!/usr/bin/env bash

set -euo pipefail
umask 077

fail() {
  echo "Preview Vault bootstrap failed: $*" >&2
  exit 1
}

require_env() {
  local name="$1"
  [ -n "${!name:-}" ] || fail "${name} is required"
}

require_env VAULT_ADDR
require_env VAULT_TOKEN

mode="${1:-}"
prefix="${2:-}"
mount="${VAULT_KV_MOUNT:-secret}"
[[ "$mode" == "ensure" || "$mode" == "delete" ]] || fail "mode must be ensure or delete"
[[ "$prefix" =~ ^scribe/previews/scribe-pr-[0-9]+@[a-z][a-z0-9-]{4,28}[a-z0-9]\.iam\.gserviceaccount\.com$ ]] || fail "prefix must identify one preview service-account namespace"
[[ "$mount" =~ ^[a-zA-Z0-9_-]+$ ]] || fail "invalid KV mount"

vault_http_retry_attempts="${VAULT_HTTP_RETRY_ATTEMPTS:-4}"
vault_http_retry_initial_delay_seconds="${VAULT_HTTP_RETRY_INITIAL_DELAY_SECONDS:-1}"
vault_http_retry_max_delay_seconds="${VAULT_HTTP_RETRY_MAX_DELAY_SECONDS:-4}"
case "$vault_http_retry_attempts" in ''|*[!0-9]*|0) fail "invalid Vault HTTP retry configuration" ;; esac
case "$vault_http_retry_initial_delay_seconds" in ''|*[!0-9]*) fail "invalid Vault HTTP retry configuration" ;; esac
case "$vault_http_retry_max_delay_seconds" in ''|*[!0-9]*) fail "invalid Vault HTTP retry configuration" ;; esac
if [ "$vault_http_retry_attempts" -gt 10 ] \
  || [ "$vault_http_retry_initial_delay_seconds" -gt 30 ] \
  || [ "$vault_http_retry_max_delay_seconds" -gt 30 ] \
  || [ "$vault_http_retry_initial_delay_seconds" -gt "$vault_http_retry_max_delay_seconds" ]; then
  fail "invalid Vault HTTP retry configuration"
fi

response_file="$(mktemp)"
status_file="$(mktemp)"
trap 'rm -f "$response_file" "$status_file"' EXIT

vault_request() {
  local method="$1"
  local endpoint="$2"
  local payload="${3:-}"
  local attempt=1
  local delay_seconds="$vault_http_retry_initial_delay_seconds"
  local -a arguments

  arguments=(
    -sS --connect-timeout 5 --max-time 30
    -o "$response_file" -w '%{http_code}'
    -X "$method"
    -H "X-Vault-Token: ${VAULT_TOKEN}"
  )
  if [ -n "${VAULT_ADMIN_TOKEN:-}" ]; then
    arguments+=(-H "X-Admin-Token: ${VAULT_ADMIN_TOKEN}")
  fi
  if [ -n "$payload" ]; then
    arguments+=(-H "Content-Type: application/json" --data "$payload")
  fi
  while [ "$attempt" -le "$vault_http_retry_attempts" ]; do
    : >"$response_file"
    : >"$status_file"
    if curl "${arguments[@]}" "${VAULT_ADDR%/}${endpoint}" >"$status_file" 2>/dev/null; then
      VAULT_STATUS="$(tr -d '\r\n' <"$status_file")"
      [[ "$VAULT_STATUS" =~ ^[0-9]{3}$ ]] || fail "Vault returned an invalid status"
      if [ "$VAULT_STATUS" -lt 500 ] || [ "$VAULT_STATUS" -ge 600 ]; then
        return 0
      fi
      if [ "$attempt" -ge "$vault_http_retry_attempts" ]; then
        fail "Vault ${method} request failed after ${vault_http_retry_attempts} attempts (HTTP ${VAULT_STATUS})"
      fi
      echo "Vault ${method} request received transient HTTP ${VAULT_STATUS}; retrying (${attempt}/${vault_http_retry_attempts})." >&2
    else
      if [ "$attempt" -ge "$vault_http_retry_attempts" ]; then
        fail "Vault ${method} request failed after ${vault_http_retry_attempts} attempts (transport error)"
      fi
      echo "Vault ${method} request encountered a transient transport error; retrying (${attempt}/${vault_http_retry_attempts})." >&2
    fi

    sleep "$delay_seconds"
    if [ "$delay_seconds" -lt "$vault_http_retry_max_delay_seconds" ]; then
      delay_seconds=$((delay_seconds * 2))
      if [ "$delay_seconds" -gt "$vault_http_retry_max_delay_seconds" ]; then
        delay_seconds="$vault_http_retry_max_delay_seconds"
      fi
    fi
    attempt=$((attempt + 1))
  done
}

ensure_secret() {
  local path="$1"
  local kind="$2"
  local payload

  vault_request GET "/v1/${mount}/data/${path}"
  if [ "$VAULT_STATUS" -ge 200 ] && [ "$VAULT_STATUS" -lt 300 ]; then
    case "$kind" in
      database)
        jq -e '.data.data.password | type == "string" and length >= 32' "$response_file" >/dev/null || fail "existing preview database secret is malformed"
        ;;
      *) fail "unknown preview secret kind" ;;
    esac
    return
  fi
  [ "$VAULT_STATUS" = "404" ] || fail "Vault could not inspect ${path} (HTTP ${VAULT_STATUS})"

  case "$kind" in
    database)
      payload="$(jq -cn --arg password "$(openssl rand -hex 32)" '{data: {password: $password}}')"
      ;;
    *) fail "unknown preview secret kind" ;;
  esac
  vault_request PUT "/v1/${mount}/data/${path}" "$payload"
  [ "$VAULT_STATUS" -ge 200 ] && [ "$VAULT_STATUS" -lt 300 ] || fail "Vault could not create ${path} (HTTP ${VAULT_STATUS})"
}

delete_secret_tree() {
  local path="$1"
  local depth="$2"
  local key
  local -a keys

  [ "$depth" -le 16 ] || fail "preview secret tree exceeds the cleanup depth limit"
  # Vault accepts both LIST and GET ?list=true for KV enumeration. Google
  # Frontend rejects the non-standard LIST verb before the Cloud Run service
  # receives it, so use the equivalent GET form on this hosted path.
  vault_request GET "/v1/${mount}/metadata/${path}?list=true"
  if [ "$VAULT_STATUS" = "404" ]; then
    return
  fi
  [ "$VAULT_STATUS" -ge 200 ] && [ "$VAULT_STATUS" -lt 300 ] || fail "Vault could not list ${path} (HTTP ${VAULT_STATUS})"
  jq -e '.data.keys | type == "array" and length <= 1000 and all(.[]; type == "string" and test("^[A-Za-z0-9._-]+/?$"))' "$response_file" >/dev/null || fail "Vault returned an unsafe preview key listing"
  mapfile -t keys < <(jq -r '.data.keys[]' "$response_file")
  for key in "${keys[@]}"; do
    if [[ "$key" == */ ]]; then
      delete_secret_tree "${path}/${key%/}" "$((depth + 1))"
    else
      vault_request DELETE "/v1/${mount}/metadata/${path}/${key}"
      if [ "$VAULT_STATUS" != "404" ] && { [ "$VAULT_STATUS" -lt 200 ] || [ "$VAULT_STATUS" -ge 300 ]; }; then
        fail "Vault could not delete ${path}/${key} (HTTP ${VAULT_STATUS})"
      fi
    fi
  done
}

case "$mode" in
  ensure)
    ensure_secret "${prefix}/database/app" database
    echo "Isolated preview bootstrap secrets are present for ${prefix}."
    ;;
  delete)
    delete_secret_tree "$prefix" 0
    echo "Preview Vault namespace ${prefix} was removed."
    ;;
esac
