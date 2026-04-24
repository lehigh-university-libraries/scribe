#!/usr/bin/env bash
# shellcheck shell=bash

set -euf -o pipefail

PROGDIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readonly PROGDIR

# Generate missing docker compose secret files without overwriting existing ones.
readonly CHARACTERS='[A-Za-z0-9]'
readonly LENGTH=32
readonly EXTERNALLY_MANAGED_SECRETS='(^|/)GOOGLE_APPLICATION_CREDENTIALS$'
readonly EXTERNALLY_MANAGED_SECRET_PLACEHOLDER='{}'
readonly DEFAULT_VAULT_KV_MOUNT='secret'
readonly DEFAULT_VAULT_DATABASE_PATH='scribe/database'

sync_database_secrets_from_vault() {
  # shellcheck disable=SC1091
  source .env
  local vault_addr vault_role vault_kv_mount vault_database_path host_uid host_gid

  vault_addr="${VAULT_ADDRESS:-${VAULT_ADDR:-}}"
  vault_addr="${vault_addr#"${vault_addr%%[![:space:]]*}"}"
  vault_addr="${vault_addr%"${vault_addr##*[![:space:]]}"}"
  if [ -z "$vault_addr" ]; then
    return 0
  fi

  vault_role="${VAULT_GCP_AUTH_ROLE:-scribe-app}"
  vault_kv_mount="${VAULT_KV_MOUNT:-$DEFAULT_VAULT_KV_MOUNT}"
  vault_database_path="${VAULT_DATABASE_PATH:-$DEFAULT_VAULT_DATABASE_PATH}"

  echo "Syncing MariaDB Docker secret files from Vault..." >&2
  host_uid="$(id -u)"
  host_gid="$(id -g)"
  VAULT_ADDRESS="$vault_addr" \
  VAULT_GCP_AUTH_ROLE="$vault_role" \
  VAULT_KV_MOUNT="$vault_kv_mount" \
  VAULT_DATABASE_PATH="$vault_database_path" \
  HOST_UID="$host_uid" \
  HOST_GID="$host_gid" \
  docker compose -f "${PROGDIR}/docker-compose.yaml" --profile init run --rm -T vault-init
}

mkdir -p "${PROGDIR}/secrets"

declare -a SECRETS
while IFS= read -r line; do
  SECRETS+=("$line")
done < <(
  docker compose -f "${PROGDIR}/docker-compose.yaml" config --format json \
    | jq -r '.secrets | to_entries[] | .value.file' \
    | uniq
)

for secret in "${SECRETS[@]}"; do
  if [[ "${secret}" =~ ${EXTERNALLY_MANAGED_SECRETS} ]]; then
    if [ ! -f "${secret}" ]; then
      echo "Creating placeholder for externally managed secret: ${secret}" >&2
      printf '%s' "${EXTERNALLY_MANAGED_SECRET_PLACEHOLDER}" > "${secret}"
    fi
    continue
  fi
  if [ ! -f "${secret}" ]; then
    echo "Creating: ${secret}" >&2
    (grep -ao "${CHARACTERS}" </dev/urandom || true) | head "-${LENGTH}" | tr -d '\n' > "${secret}"
  fi
done

sync_database_secrets_from_vault
