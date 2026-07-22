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

response_file="$(mktemp)"
status_file="$(mktemp)"
trap 'rm -f "$response_file" "$status_file"' EXIT

vault_request() {
  local method="$1"
  local endpoint="$2"
  local payload="${3:-}"
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
  : >"$response_file"
  : >"$status_file"
  if ! curl "${arguments[@]}" "${VAULT_ADDR%/}${endpoint}" >"$status_file"; then
    fail "Vault ${method} request failed"
  fi
  VAULT_STATUS="$(tr -d '\r\n' <"$status_file")"
  [[ "$VAULT_STATUS" =~ ^[0-9]{3}$ ]] || fail "Vault returned an invalid status"
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
  vault_request LIST "/v1/${mount}/metadata/${path}"
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
