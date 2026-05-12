#!/bin/sh

set -eu

require_env() {
  name="$1"
  eval "value=\${$name:-}"
  if [ -z "$value" ]; then
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

  if curl -fsS -o "$response_file" -w '%{http_code}' \
    -H "X-Vault-Token: $VAULT_TOKEN" \
    "$endpoint" >"$status_file"; then
    return 0
  fi
  return 1
}

write_secret_file() {
  destination="$1"
  value="$2"
  tmp_file="$(mktemp)"

  printf '%s' "$value" > "$tmp_file"
  mv "$tmp_file" "$destination"

  if [ -n "${HOST_UID:-}" ] && [ -n "${HOST_GID:-}" ]; then
    chown "${HOST_UID}:${HOST_GID}" "$destination"
  fi
}

require_env "VAULT_ADDRESS"
require_env "VAULT_GCP_AUTH_ROLE"
require_env "GOOGLE_APPLICATION_CREDENTIALS"

VAULT_KV_MOUNT="${VAULT_KV_MOUNT:-secret}"
VAULT_DATABASE_APP_PATH="${VAULT_DATABASE_APP_PATH:-${VAULT_DATABASE_PATH:-scribe/database/app}}"
VAULT_DATABASE_ROOT_PATH="${VAULT_DATABASE_ROOT_PATH:-scribe/database/root}"
OUT_DIR="${OUT_DIR:-/out}"

apk add --no-cache curl jq openssl >/dev/null

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

login_response="$(
  curl -fsS \
    -H "Content-Type: application/json" \
    -X POST \
    -d "$(jq -cn --arg role "$VAULT_GCP_AUTH_ROLE" --arg jwt "${unsigned}.${signature}" '{role: $role, jwt: $jwt}')" \
    "${VAULT_ADDRESS%/}/v1/auth/gcp/login"
)"
VAULT_TOKEN="$(printf '%s' "$login_response" | jq -r '.auth.client_token // empty')"
if [ -z "$VAULT_TOKEN" ]; then
  echo "Vault GCP login response did not include auth.client_token." >&2
  exit 1
fi

secret_response="$(mktemp)"
secret_status="$(mktemp)"
trap 'rm -f "$key_file" "$secret_response" "$secret_status"' EXIT INT TERM

v2_endpoint="${VAULT_ADDRESS%/}/v1/${VAULT_KV_MOUNT}/data/${VAULT_DATABASE_APP_PATH}"

if ! vault_read_secret "$v2_endpoint" "$secret_response" "$secret_status"; then
	cat "$secret_response" >&2
	echo "Failed to read Vault database app secret from ${VAULT_KV_MOUNT}/data/${VAULT_DATABASE_APP_PATH}" >&2
	exit 1
fi
password="$(jq -r '.data.data.password // empty' "$secret_response")"
if [ -z "$password" ]; then
  echo "Vault database app secret ${VAULT_KV_MOUNT}/${VAULT_DATABASE_APP_PATH} is missing password." >&2
  exit 1
fi

v2_endpoint="${VAULT_ADDRESS%/}/v1/${VAULT_KV_MOUNT}/data/${VAULT_DATABASE_ROOT_PATH}"
if ! vault_read_secret "$v2_endpoint" "$secret_response" "$secret_status"; then
  cat "$secret_response" >&2
  echo "Failed to read Vault database root secret from ${VAULT_KV_MOUNT}/data/${VAULT_DATABASE_ROOT_PATH}" >&2
  exit 1
fi
root_password="$(jq -r '.data.data.root_password // empty' "$secret_response")"
if [ -z "$root_password" ]; then
  echo "Vault database root secret ${VAULT_KV_MOUNT}/${VAULT_DATABASE_ROOT_PATH} is missing root_password." >&2
  exit 1
fi

mkdir -p "$OUT_DIR"
write_secret_file "${OUT_DIR}/mariadb_password" "$password"
write_secret_file "${OUT_DIR}/mariadb_root_password" "$root_password"
