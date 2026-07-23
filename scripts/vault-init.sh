#!/bin/sh

set -eu

trace_app_init_stage() {
  case "${1:-}" in
    vault-script-start | \
      vault-helper-unavailable | \
      vault-helper-ready | \
      vault-settings-invalid | \
      vault-settings-ready | \
      vault-credentials-missing | \
      vault-credentials-invalid | \
      vault-credentials-ready | \
      vault-jwt-failed | \
      vault-jwt-ready | \
      vault-login-start | \
      vault-login-failed | \
      vault-login-complete | \
      vault-database-read-start | \
      vault-database-read-failed | \
      vault-database-value-missing | \
      vault-database-read-complete | \
      vault-output-write-start | \
      vault-output-write-failed | \
      vault-output-write-complete)
      printf 'SCRIBE_APP_INIT_TRACE_V1 stage=%s\n' "$1" || :
      ;;
    *) return 1 ;;
  esac
}

trace_app_init_stage vault-script-start
VAULT_RETRY_LIB="${VAULT_RETRY_LIB:-/usr/local/lib/scribe/vault-retry.sh}"
if [ ! -r "$VAULT_RETRY_LIB" ] ||
  ! /bin/sh -n "$VAULT_RETRY_LIB" 2>/dev/null; then
  trace_app_init_stage vault-helper-unavailable
  echo "Vault retry helper is unavailable." >&2
  exit 1
fi
# shellcheck disable=SC1090 # The deployment intentionally supports an absolute override.
if ! . "$VAULT_RETRY_LIB"; then
  trace_app_init_stage vault-helper-unavailable
  echo "Vault retry helper is unavailable." >&2
  exit 1
fi
trace_app_init_stage vault-helper-ready

key_file=""
signature_file=""
login_response=""
login_status=""
secret_response=""
secret_status=""
output_temp_file=""
cleanup_temp_files() {
  for temp_file in \
    "$key_file" \
    "$signature_file" \
    "$login_response" \
    "$login_status" \
    "$secret_response" \
    "$secret_status" \
    "$output_temp_file"; do
    if [ -n "$temp_file" ]; then
      rm -f "$temp_file" || :
    fi
  done
}
terminate_after_cleanup() {
  signal_status="$1"
  trap - EXIT INT TERM
  cleanup_temp_files
  exit "$signal_status"
}
trap cleanup_temp_files EXIT
trap 'terminate_after_cleanup 130' INT
trap 'terminate_after_cleanup 143' TERM

require_env() {
  name="$1"
  if ! value="$(printenv "$name")" || [ -z "$value" ]; then
    trace_app_init_stage vault-settings-invalid
    echo "Missing required environment variable: $name" >&2
    exit 1
  fi
}

base64url() {
  base64_value="$(openssl base64 -A)" || return 1
  [ -n "$base64_value" ] || return 1
  printf '%s' "$base64_value" |
    sed -e 's/+/-/g' -e 's#/#_#g' -e 's/=//g'
}

vault_read_secret() {
  endpoint="$1"
  response_file="$2"
  status_file="$3"
  if [ -n "${VAULT_ADMIN_TOKEN:-}" ]; then
    if curl -fsS --connect-timeout 5 --max-time 30 -o "$response_file" -w '%{http_code}' \
      -H "X-Vault-Token: $VAULT_TOKEN" \
      -H "X-Admin-Token: $VAULT_ADMIN_TOKEN" \
      "$endpoint" >"$status_file"; then
      return 0
    fi
    return 1
  fi

  if curl -fsS --connect-timeout 5 --max-time 30 -o "$response_file" -w '%{http_code}' \
    -H "X-Vault-Token: $VAULT_TOKEN" \
    "$endpoint" >"$status_file"; then
    return 0
  fi
  return 1
}

write_secret_file() {
  destination="$1"
  value="$2"
  output_temp_file="$(mktemp "${destination}.tmp.XXXXXX")" || return 1

  if ! printf '%s' "$value" >"$output_temp_file"; then
    rm -f "$output_temp_file" || :
    return 1
  fi
  if [ -n "${HOST_UID:-}" ] && [ -n "${HOST_GID:-}" ]; then
    if ! chown "${HOST_UID}:${HOST_GID}" "$output_temp_file"; then
      rm -f "$output_temp_file" || :
      return 1
    fi
  fi
  if ! mv "$output_temp_file" "$destination"; then
    rm -f "$output_temp_file" || :
    return 1
  fi
  output_temp_file=""
}

require_env "VAULT_ADDRESS"
require_env "VAULT_GCP_AUTH_ROLE"
require_env "GOOGLE_APPLICATION_CREDENTIALS"

VAULT_KV_MOUNT="${VAULT_KV_MOUNT:-secret}"
VAULT_SECRET_PREFIX="${VAULT_SECRET_PREFIX:-scribe/dev}"
VAULT_DATABASE_APP_PATH="${VAULT_DATABASE_APP_PATH:-${VAULT_DATABASE_PATH:-${VAULT_SECRET_PREFIX}/database/app}}"
OUT_DIR="${OUT_DIR:-/out}"
trace_app_init_stage vault-settings-ready

if [ ! -f "$GOOGLE_APPLICATION_CREDENTIALS" ]; then
  trace_app_init_stage vault-credentials-missing
  echo "Credentials file not found: $GOOGLE_APPLICATION_CREDENTIALS" >&2
  exit 1
fi

key_file="$(mktemp)" || {
  trace_app_init_stage vault-credentials-invalid
  echo "Unable to prepare service account credentials." >&2
  exit 1
}
if ! client_email="$(jq -r '.client_email // empty' "$GOOGLE_APPLICATION_CREDENTIALS")" ||
  ! private_key_id="$(jq -r '.private_key_id // empty' "$GOOGLE_APPLICATION_CREDENTIALS")" ||
  ! jq -r '.private_key // empty' "$GOOGLE_APPLICATION_CREDENTIALS" >"$key_file"; then
  trace_app_init_stage vault-credentials-invalid
  echo "Invalid service account credentials." >&2
  exit 1
fi

if [ -z "$client_email" ] || [ -z "$private_key_id" ] || [ ! -s "$key_file" ]; then
  trace_app_init_stage vault-credentials-invalid
  echo "Incomplete service account credentials in $GOOGLE_APPLICATION_CREDENTIALS" >&2
  exit 1
fi
trace_app_init_stage vault-credentials-ready

build_vault_jwt() {
  now="$(date +%s)" || return 1
  case "$now" in '' | *[!0-9]*) return 1 ;; esac
  exp="$((now + 600))"
  header="$(jq -cn --arg kid "$private_key_id" '{alg:"RS256", kid:$kid, typ:"JWT"}')" || return 1
  claims="$(jq -cn \
    --arg aud "vault/${VAULT_GCP_AUTH_ROLE}" \
    --arg iss "$client_email" \
    --arg sub "$client_email" \
    --argjson iat "$now" \
    --argjson exp "$exp" \
    '{aud: $aud, exp: $exp, iat: $iat, iss: $iss, sub: $sub}')" || return 1
  encoded_header="$(printf '%s' "$header" | base64url)" || return 1
  encoded_claims="$(printf '%s' "$claims" | base64url)" || return 1
  [ -n "$encoded_header" ] && [ -n "$encoded_claims" ] || return 1
  unsigned="${encoded_header}.${encoded_claims}"
  signature_file="$(mktemp)" || return 1
  if ! printf '%s' "$unsigned" |
    openssl dgst -binary -sha256 -sign "$key_file" >"$signature_file"; then
    rm -f "$signature_file"
    return 1
  fi
  if ! signature="$(base64url <"$signature_file")"; then
    rm -f "$signature_file"
    return 1
  fi
  if ! rm -f "$signature_file"; then
    return 1
  fi
  signature_file=""
  [ -n "$signature" ]
}
if ! build_vault_jwt; then
  trace_app_init_stage vault-jwt-failed
  echo "Unable to construct Vault authentication JWT." >&2
  exit 1
fi
trace_app_init_stage vault-jwt-ready

trace_app_init_stage vault-login-start
if ! login_response="$(mktemp)" ||
  ! login_status="$(mktemp)"; then
  trace_app_init_stage vault-login-failed
  echo "Unable to prepare the Vault login response." >&2
  exit 1
fi
login_once() {
  : >"$login_response"
  : >"$login_status"
  if ! curl -sS \
    --connect-timeout 5 \
    --max-time 30 \
    -o "$login_response" \
    -w '%{http_code}' \
    -H "Content-Type: application/json" \
    -X POST \
    -d "$(jq -cn --arg role "$VAULT_GCP_AUTH_ROLE" --arg jwt "${unsigned}.${signature}" '{role: $role, jwt: $jwt}')" \
    "${VAULT_ADDRESS%/}/v1/auth/gcp/login" >"$login_status"; then
    return 1
  fi
  status="$(tr -d '\r\n' <"$login_status")"
  case "$status" in
    2??) ;;
    *) return 1 ;;
  esac
  VAULT_TOKEN="$(jq -r '.auth.client_token // empty' "$login_response")"
  [ -n "$VAULT_TOKEN" ]
}
if ! vault_retry "Vault GCP login" login_once; then
  trace_app_init_stage vault-login-failed
  echo "Vault GCP login did not become available; response body withheld." >&2
  exit 1
fi
trace_app_init_stage vault-login-complete
export VAULT_TOKEN

trace_app_init_stage vault-database-read-start
if ! secret_response="$(mktemp)" ||
  ! secret_status="$(mktemp)"; then
  trace_app_init_stage vault-database-read-failed
  echo "Unable to prepare the Vault database app secret response." >&2
  exit 1
fi

v2_endpoint="${VAULT_ADDRESS%/}/v1/${VAULT_KV_MOUNT}/data/${VAULT_DATABASE_APP_PATH}"

read_database_secret_once() {
  vault_read_secret "$v2_endpoint" "$secret_response" "$secret_status"
}
if ! vault_retry "Vault database app secret read" read_database_secret_once; then
  trace_app_init_stage vault-database-read-failed
  echo "Failed to read Vault database app secret from ${VAULT_KV_MOUNT}/data/${VAULT_DATABASE_APP_PATH}; response body withheld." >&2
  exit 1
fi
if ! password="$(jq -r '.data.data.password // empty' "$secret_response")"; then
  trace_app_init_stage vault-database-read-failed
  echo "Vault database app secret response was invalid; response body withheld." >&2
  exit 1
fi
if [ -z "$password" ]; then
  trace_app_init_stage vault-database-value-missing
  echo "Vault database app secret ${VAULT_KV_MOUNT}/${VAULT_DATABASE_APP_PATH} is missing password." >&2
  exit 1
fi
trace_app_init_stage vault-database-read-complete

trace_app_init_stage vault-output-write-start
if ! mkdir -p "$OUT_DIR" ||
  ! write_secret_file "${OUT_DIR}/mariadb_password" "$password"; then
  trace_app_init_stage vault-output-write-failed
  echo "Unable to materialize the Vault database app credential." >&2
  exit 1
fi
trace_app_init_stage vault-output-write-complete
