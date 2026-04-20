#!/usr/bin/env bash

set -euo pipefail

readonly DEFAULT_PREFIX="scribe"
readonly DEFAULT_MOUNT="secret"

usage() {
  cat <<'EOF'
Usage:
  make vault-secrets
  ./ci/vault-secrets.sh

Environment overrides:
  SCRIBE_VAULT_DEV_ADDR   Default Vault URL to use when "dev" is selected
  SCRIBE_VAULT_PROD_ADDR  Default Vault URL to use when "prod" is selected
  VAULT_ADDR              Explicit Vault URL override for either environment
  VAULT_ADMIN_TOKEN       Explicit admin access token override
  VAULT_MOUNT             Vault KV mount name (default: secret)

The script uses the Vault HTTP API through the OAuth-protected admin header:
  X-Admin-Token: <gcloud auth print-access-token>
EOF
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "Missing required command: $1" >&2
    exit 1
  }
}

trim() {
  local value="$1"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "$value"
}

prompt() {
  local label="$1"
  local default_value="${2:-}"
  local answer=""
  if [ -n "$default_value" ]; then
    read -r -p "$label [$default_value]: " answer || true
    answer="$(trim "${answer:-}")"
    if [ -z "$answer" ]; then
      answer="$default_value"
    fi
  else
    read -r -p "$label: " answer || true
    answer="$(trim "${answer:-}")"
  fi
  printf '%s' "$answer"
}

prompt_secret() {
  local label="$1"
  local answer=""
  read -r -s -p "$label: " answer || true
  echo
  printf '%s' "$(trim "${answer:-}")"
}

json_escape() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  value="${value//$'\n'/\\n}"
  value="${value//$'\r'/\\r}"
  value="${value//$'\t'/\\t}"
  printf '%s' "$value"
}

vault_request() {
  local method="$1"
  local path="$2"
  local query="${3:-}"
  local body="${4:-}"

  local url="${VAULT_ADDR%/}/v1/${VAULT_MOUNT}/${path}"
  if [ -n "$query" ]; then
    url="${url}?${query}"
  fi

  local tmp_file
  tmp_file="$(mktemp)"
  local status
  if [ -n "$body" ]; then
    status="$(curl -sS -o "$tmp_file" -w '%{http_code}' \
      -X "$method" \
      -H "Accept: application/json" \
      -H "Content-Type: application/json" \
      -H "Authorization: Bearer ${VAULT_ADMIN_TOKEN}" \
      -H "X-Admin-Token: ${VAULT_ADMIN_TOKEN}" \
      --data "$body" \
      "$url")"
  else
    status="$(curl -sS -o "$tmp_file" -w '%{http_code}' \
      -X "$method" \
      -H "Accept: application/json" \
      -H "Authorization: Bearer ${VAULT_ADMIN_TOKEN}" \
      -H "X-Admin-Token: ${VAULT_ADMIN_TOKEN}" \
      "$url")"
  fi
  VAULT_LAST_STATUS="$status"
  cat "$tmp_file"
  rm -f "$tmp_file"
}

vault_read_data_object() {
  local path="$1"
  local response
  response="$(vault_request GET "$path")"
  case "$VAULT_LAST_STATUS" in
    200) ;;
    404)
      printf '{}'
      return 0
      ;;
    *)
      echo "$response" >&2
      echo "Vault read failed for ${path} (HTTP ${VAULT_LAST_STATUS})" >&2
      return 1
      ;;
  esac
  printf '%s' "$response" | jq -c '.data // {}'
}

vault_write_object() {
  local path="$1"
  local object_json="$2"
  local response
  response="$(vault_request POST "$path" "" "$object_json")"
  case "$VAULT_LAST_STATUS" in
    200|204) ;;
    *)
      echo "$response" >&2
      echo "Vault write failed for ${path} (HTTP ${VAULT_LAST_STATUS})" >&2
      return 1
      ;;
  esac
}

vault_list_keys() {
  local prefix="$1"
  local response
  response="$(vault_request GET "$prefix" "list=true")"
  case "$VAULT_LAST_STATUS" in
    200)
      printf '%s' "$response" | jq -r '.data.keys[]?'
      ;;
    404)
      return 0
      ;;
    *)
      echo "$response" >&2
      echo "Vault list failed for ${prefix} (HTTP ${VAULT_LAST_STATUS})" >&2
      return 1
      ;;
  esac
}

list_recursive() {
  local prefix="$1"
  local key=""
  while IFS= read -r key; do
    [ -n "$key" ] || continue
    if [[ "$key" == */ ]]; then
      list_recursive "${prefix%/}/${key%/}"
    else
      printf '%s\n' "${prefix%/}/${key}"
    fi
  done < <(vault_list_keys "$prefix")
}

read_secret_pretty() {
  local path="$1"
  local data
  data="$(vault_read_data_object "$path")"
  printf '%s\n' "== ${path} =="
  printf '%s\n' "$data" | jq .
}

update_required_secret() {
  local path="$1"
  shift

  local existing_json
  existing_json="$(vault_read_data_object "$path")"
  local payload="$existing_json"
  local changed=0
  local field=""
  local label=""
  local mode=""
  local value=""

  while [ $# -gt 0 ]; do
    field="$1"
    label="$2"
    mode="$3"
    shift 3

    if [ "$mode" = "secret" ]; then
      value="$(prompt_secret "${label} (leave blank to keep current)")"
    else
      value="$(prompt "${label} (leave blank to keep current)")"
    fi
    if [ -z "$value" ]; then
      continue
    fi
    payload="$(printf '%s' "$payload" | jq --arg key "$field" --arg value "$value" '. + {($key): $value}')"
    changed=1
  done

  if [ "$changed" -eq 0 ]; then
    echo "Skipping ${path}; no new values supplied."
    return 0
  fi

  vault_write_object "$path" "$payload"
  echo "Updated ${path}"
}

select_environment() {
  local answer
  while true; do
    answer="$(prompt "Target environment (dev/prod)" "dev")"
    case "$(printf '%s' "$answer" | tr '[:upper:]' '[:lower:]')" in
      dev|prod)
        TARGET_ENV="$(printf '%s' "$answer" | tr '[:upper:]' '[:lower:]')"
        return 0
        ;;
    esac
    echo "Enter 'dev' or 'prod'."
  done
}

select_action() {
  local answer
  while true; do
    answer="$(prompt "Action (update/list/read/read-all)" "update")"
    case "$(printf '%s' "$answer" | tr '[:upper:]' '[:lower:]')" in
      update|list|read|read-all)
        ACTION="$(printf '%s' "$answer" | tr '[:upper:]' '[:lower:]')"
        return 0
        ;;
    esac
    echo "Enter one of: update, list, read, read-all."
  done
}

resolve_vault_addr() {
  local default_addr=""
  if [ -n "${VAULT_ADDR:-}" ]; then
    default_addr="${VAULT_ADDR}"
  elif [ "$TARGET_ENV" = "prod" ]; then
    default_addr="${SCRIBE_VAULT_PROD_ADDR:-}"
  else
    default_addr="${SCRIBE_VAULT_DEV_ADDR:-}"
  fi
  VAULT_ADDR="$(prompt "Vault URL for ${TARGET_ENV}" "$default_addr")"
  if [ -z "$VAULT_ADDR" ]; then
    echo "Vault URL is required." >&2
    exit 1
  fi
}

resolve_vault_token() {
  local default_token="${VAULT_ADMIN_TOKEN:-}"
  if [ -z "$default_token" ] && command -v gcloud >/dev/null 2>&1; then
    default_token="$(gcloud auth print-access-token 2>/dev/null || true)"
  fi
  if [ -n "$default_token" ]; then
    local answer=""
    read -r -s -p "Admin access token (press enter to use current gcloud token): " answer || true
    echo
    answer="$(trim "${answer:-}")"
    if [ -n "$answer" ]; then
      VAULT_ADMIN_TOKEN="$answer"
    else
      VAULT_ADMIN_TOKEN="$default_token"
    fi
  else
    VAULT_ADMIN_TOKEN="$(prompt_secret "Admin access token")"
  fi
  if [ -z "${VAULT_ADMIN_TOKEN:-}" ]; then
    echo "Admin access token is required." >&2
    exit 1
  fi
}

run_update() {
  echo "Updating required application secrets under ${VAULT_MOUNT}/${DEFAULT_PREFIX}"
  update_required_secret \
    "${DEFAULT_PREFIX}/google_oauth" \
    "client_id" "Google OAuth client ID" "plain" \
    "client_secret" "Google OAuth client secret" "secret"
  update_required_secret \
    "${DEFAULT_PREFIX}/openai" \
    "api_key" "OpenAI API key" "secret"
  update_required_secret \
    "${DEFAULT_PREFIX}/gemini" \
    "api_key" "Gemini API key" "secret"
  update_required_secret \
    "${DEFAULT_PREFIX}/database" \
    "password" "Application database password" "secret" \
    "root_password" "MariaDB root password" "secret"
}

run_list() {
  local prefix
  prefix="$(prompt "List prefix" "$DEFAULT_PREFIX")"
  list_recursive "$prefix"
}

run_read() {
  local path
  path="$(prompt "Secret path to read" "${DEFAULT_PREFIX}/google_oauth")"
  read_secret_pretty "$path"
}

run_read_all() {
  local prefix
  local path=""
  prefix="$(prompt "Read-all prefix" "$DEFAULT_PREFIX")"
  while IFS= read -r path; do
    [ -n "$path" ] || continue
    read_secret_pretty "$path"
  done < <(list_recursive "$prefix")
}

main() {
  if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
    usage
    exit 0
  fi

  require_cmd curl
  require_cmd jq

  VAULT_MOUNT="${VAULT_MOUNT:-$DEFAULT_MOUNT}"
  select_environment
  select_action
  resolve_vault_addr
  resolve_vault_token

  case "$ACTION" in
    update) run_update ;;
    list) run_list ;;
    read) run_read ;;
    read-all) run_read_all ;;
  esac
}

main "$@"
