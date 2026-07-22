#!/bin/sh

set -eu

VAULT_RETRY_LIB="${VAULT_RETRY_LIB:-/usr/local/lib/scribe/vault-retry.sh}"
if [ ! -r "$VAULT_RETRY_LIB" ]; then
  echo "Vault retry helper is unavailable." >&2
  exit 1
fi
# shellcheck disable=SC1090 # The deployment intentionally supports an absolute override.
. "$VAULT_RETRY_LIB"

require_env() {
  name="$1"
  if ! value="$(printenv "$name")" || [ -z "$value" ]; then
    echo "Missing required environment variable: $name" >&2
    exit 1
  fi
}

base64url() {
  openssl base64 -A | tr '+/' '-_' | tr -d '='
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
  tmp_file="$(mktemp "${destination}.tmp.XXXXXX")"

  printf '%s' "$value" > "$tmp_file"
  if [ -n "${HOST_UID:-}" ] && [ -n "${HOST_GID:-}" ]; then
    chown "${HOST_UID}:${HOST_GID}" "$tmp_file"
  fi
  mv "$tmp_file" "$destination"
}

require_env "VAULT_ADDRESS"
require_env "VAULT_GCP_AUTH_ROLE"
require_env "GOOGLE_APPLICATION_CREDENTIALS"

VAULT_KV_MOUNT="${VAULT_KV_MOUNT:-secret}"
VAULT_SECRET_PREFIX="${VAULT_SECRET_PREFIX:-scribe/dev}"
VAULT_DATABASE_APP_PATH="${VAULT_DATABASE_APP_PATH:-${VAULT_DATABASE_PATH:-${VAULT_SECRET_PREFIX}/database/app}}"
OUT_DIR="${OUT_DIR:-/out}"

if [ ! -f "$GOOGLE_APPLICATION_CREDENTIALS" ]; then
  echo "Credentials file not found: $GOOGLE_APPLICATION_CREDENTIALS" >&2
  exit 1
fi

client_email="$(jq -r '.client_email // empty' "$GOOGLE_APPLICATION_CREDENTIALS")"
private_key_id="$(jq -r '.private_key_id // empty' "$GOOGLE_APPLICATION_CREDENTIALS")"

key_file="$(mktemp)"
trap 'rm -f "$key_file"' EXIT INT TERM
jq -r '.private_key // empty' "$GOOGLE_APPLICATION_CREDENTIALS" > "$key_file"

if [ -z "$client_email" ] || [ -z "$private_key_id" ] || [ ! -s "$key_file" ]; then
  echo "Incomplete service account credentials in $GOOGLE_APPLICATION_CREDENTIALS" >&2
  exit 1
fi

now="$(date +%s)"
exp="$((now + 600))"
header="$(jq -cn --arg kid "$private_key_id" '{alg:"RS256", kid:$kid, typ:"JWT"}')"
claims="$(jq -cn \
  --arg aud "vault/${VAULT_GCP_AUTH_ROLE}" \
  --arg iss "$client_email" \
  --arg sub "$client_email" \
  --argjson iat "$now" \
  --argjson exp "$exp" \
  '{aud: $aud, exp: $exp, iat: $iat, iss: $iss, sub: $sub}')"

unsigned="$(
  printf '%s' "$header" | base64url
  printf '.'
  printf '%s' "$claims" | base64url
)"
signature="$(
  printf '%s' "$unsigned" \
    | openssl dgst -binary -sha256 -sign "$key_file" \
    | base64url
)"

login_response="$(mktemp)"
login_status="$(mktemp)"
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
  echo "Vault GCP login did not become available; response body withheld." >&2
  exit 1
fi
export VAULT_TOKEN

secret_response="$(mktemp)"
secret_status="$(mktemp)"
trap 'rm -f "$key_file" "$login_response" "$login_status" "$secret_response" "$secret_status"' EXIT INT TERM

v2_endpoint="${VAULT_ADDRESS%/}/v1/${VAULT_KV_MOUNT}/data/${VAULT_DATABASE_APP_PATH}"

read_database_secret_once() {
  vault_read_secret "$v2_endpoint" "$secret_response" "$secret_status"
}
if ! vault_retry "Vault database app secret read" read_database_secret_once; then
  echo "Failed to read Vault database app secret from ${VAULT_KV_MOUNT}/data/${VAULT_DATABASE_APP_PATH}; response body withheld." >&2
  exit 1
fi
password="$(jq -r '.data.data.password // empty' "$secret_response")"
if [ -z "$password" ]; then
  echo "Vault database app secret ${VAULT_KV_MOUNT}/${VAULT_DATABASE_APP_PATH} is missing password." >&2
  exit 1
fi

mkdir -p "$OUT_DIR"
write_secret_file "${OUT_DIR}/mariadb_password" "$password"
