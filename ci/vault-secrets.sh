#!/usr/bin/env bash

set -euo pipefail

readonly DEFAULT_PREFIX="${VAULT_SECRET_PREFIX:-scribe/dev}"
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
  VAULT_TOKEN             Explicit Vault client token override
  VAULT_GCLOUD_ACCOUNT    Gcloud account to use for admin Vault login
  VAULT_JWT_AUDIENCE      Google ID token audience for Vault login
  VAULT_JWT_ROLE          Explicit Vault JWT role override
  VAULT_MOUNT             Vault KV mount name (default: secret)

The script talks to Vault through two layers of auth:
  X-Admin-Token: <gcloud auth print-access-token>   # proxy access
  X-Vault-Token: <vault token>                      # Vault ACL access

If VAULT_TOKEN is not supplied, the helper logs into Vault through
/v1/auth/google-jwt/login using the active gcloud identity.
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
  printf '\n' >&2
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

sanitize_role_component() {
  local value="$1"
  printf '%s' "$value" | sed 's/@/-at-/g; s/\./-/g'
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
  local curl_args=(
    -sS
    -o "$tmp_file"
    -w '%{http_code}'
    -X "$method"
    -H "Accept: application/json"
    -H "X-Admin-Token: ${VAULT_ADMIN_TOKEN}"
  )
  if [ -n "${VAULT_TOKEN:-}" ]; then
    curl_args+=(-H "X-Vault-Token: ${VAULT_TOKEN}")
  fi
  if [ -n "$body" ]; then
    curl_args+=(
      -H "Content-Type: application/json"
      --data "$body"
    )
  else
    :
  fi
  status="$(curl "${curl_args[@]}" "$url")"
  VAULT_LAST_STATUS="$status"
  VAULT_LAST_RESPONSE="$(cat "$tmp_file")"
  rm -f "$tmp_file"
}

vault_data_path() {
  printf 'data/%s' "$1"
}

vault_metadata_path() {
  printf 'metadata/%s' "$1"
}

vault_read_data_object() {
  local path="$1"
  vault_request GET "$(vault_data_path "$path")"
  case "$VAULT_LAST_STATUS" in
    200) ;;
    404)
      printf '{}'
      return 0
      ;;
    *)
      echo "$VAULT_LAST_RESPONSE" >&2
      echo "Vault read failed for ${path} (HTTP ${VAULT_LAST_STATUS})" >&2
      return 1
      ;;
  esac
  printf '%s' "$VAULT_LAST_RESPONSE" | jq -c '.data.data // {}'
}

vault_write_object() {
  local path="$1"
  local object_json="$2"
  local body
  body="$(printf '%s' "$object_json" | jq -c '{data: .}')"
  vault_request POST "$(vault_data_path "$path")" "" "$body"
  case "$VAULT_LAST_STATUS" in
    200|204) ;;
    *)
      echo "$VAULT_LAST_RESPONSE" >&2
      echo "Vault write failed for ${path} (HTTP ${VAULT_LAST_STATUS})" >&2
      return 1
      ;;
  esac
}

vault_list_keys() {
  local prefix="$1"
  vault_request GET "$(vault_metadata_path "$prefix")" "list=true"
  case "$VAULT_LAST_STATUS" in
    200)
      printf '%s' "$VAULT_LAST_RESPONSE" | jq -r '.data.keys[]?'
      ;;
    404)
      return 0
      ;;
    *)
      echo "$VAULT_LAST_RESPONSE" >&2
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

resolve_vault_client_token() {
  if [ -n "${VAULT_TOKEN:-}" ]; then
    return 0
  fi
  if ! command -v gcloud >/dev/null 2>&1; then
    echo "gcloud is required to log into Vault as an admin when VAULT_TOKEN is not set." >&2
    exit 1
  fi

  local gcloud_account="${VAULT_GCLOUD_ACCOUNT:-}"
  if [ -z "$gcloud_account" ]; then
    gcloud_account="$(gcloud config get-value account 2>/dev/null || true)"
  fi
  gcloud_account="$(trim "${gcloud_account:-}")"
  if [ -z "$gcloud_account" ]; then
    echo "Could not determine an active gcloud account. Set VAULT_GCLOUD_ACCOUNT or run gcloud auth login first." >&2
    exit 1
  fi

  local jwt_role="${VAULT_JWT_ROLE:-admin-$(sanitize_role_component "$gcloud_account")}"
  local id_token=""
  id_token="$(gcloud auth print-identity-token "$gcloud_account" 2>/dev/null || true)"
  id_token="$(trim "${id_token:-}")"
  if [ -z "$id_token" ]; then
    echo "Failed to mint a Google ID token for ${gcloud_account}. Run gcloud auth login again or set VAULT_TOKEN explicitly." >&2
    exit 1
  fi

  local response_file
  response_file="$(mktemp)"
  local status_code
  status_code="$(
    curl -sS -o "$response_file" -w '%{http_code}' \
      -X POST \
      -H "Content-Type: application/json" \
      -H "X-Admin-Token: ${VAULT_ADMIN_TOKEN}" \
      --data "$(jq -cn --arg role "$jwt_role" --arg jwt "$id_token" '{role: $role, jwt: $jwt}')" \
      "${VAULT_ADDR%/}/v1/auth/google-jwt/login"
  )"
  if [ "$status_code" -lt 200 ] || [ "$status_code" -ge 300 ]; then
    cat "$response_file" >&2
    rm -f "$response_file"
    echo "Vault admin login failed for role ${jwt_role} using gcloud account ${gcloud_account} (HTTP ${status_code})." >&2
    exit 1
  fi

  VAULT_TOKEN="$(jq -r '.auth.client_token // empty' "$response_file")"
  rm -f "$response_file"
  if [ -z "${VAULT_TOKEN:-}" ]; then
    echo "Vault admin login succeeded but did not return auth.client_token." >&2
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
    "${DEFAULT_PREFIX}/database/app" \
    "password" "Application database password" "secret"
  update_required_secret \
    "${DEFAULT_PREFIX}/database/root" \
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
  require_cmd gcloud
  require_cmd jq

  VAULT_MOUNT="${VAULT_MOUNT:-$DEFAULT_MOUNT}"
  VAULT_LAST_STATUS=""
  VAULT_LAST_RESPONSE=""
  select_environment
  select_action
  resolve_vault_addr
  resolve_vault_token
  resolve_vault_client_token

  case "$ACTION" in
    update) run_update ;;
    list) run_list ;;
    read) run_read ;;
    read-all) run_read_all ;;
  esac
}

main "$@"
