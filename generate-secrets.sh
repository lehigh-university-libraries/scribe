#!/usr/bin/env bash
# shellcheck shell=bash

set -euf -o pipefail
umask 077

PROGDIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readonly PROGDIR

# Generate missing docker compose secret files without overwriting existing ones.
readonly CHARACTERS='[A-Za-z0-9]'
readonly LENGTH=32
readonly EXTERNALLY_MANAGED_SECRETS='(^|/)GOOGLE_APPLICATION_CREDENTIALS$'
readonly EXTERNALLY_MANAGED_SECRET_PLACEHOLDER='{}'
readonly DEFAULT_VAULT_KV_MOUNT='secret'
readonly DEFAULT_VAULT_SECRET_PREFIX='scribe/dev'

normalize_secret_permissions() {
  local secrets_dir="${PROGDIR}/secrets"

  if command -v chgrp >/dev/null 2>&1; then
    if ! chgrp -R "${SECRETS_GID}" "${secrets_dir}" 2>/dev/null; then
      echo "Warning: could not set ${secrets_dir} group to ${SECRETS_GID}; continuing with existing group." >&2
    fi
  fi

  find "${secrets_dir}" -type d -exec chmod 750 {} +
  find "${secrets_dir}" -type f -exec chmod 640 {} +
}

persist_compose_secret_group() {
  local env_file="${PROGDIR}/.env"
  local temp_file

  if [ ! -f "${env_file}" ]; then
    return 0
  fi
  temp_file="$(mktemp "${PROGDIR}/.env.XXXXXX")"
  if ! awk -v value="${SCRIBE_SECRETS_GID}" '
    !/^SCRIBE_SECRETS_GID=/ { print }
    END { print "SCRIBE_SECRETS_GID=" value }
  ' "${env_file}" > "${temp_file}"; then
    rm -f "${temp_file}"
    return 1
  fi
  chmod 600 "${temp_file}"
  mv -f "${temp_file}" "${env_file}"
}

sync_database_secrets_from_vault() {
  local compose_config vault_addr vault_role vault_kv_mount vault_secret_prefix vault_database_app_path host_uid host_gid

  # Compose excludes profiled services from `config` unless the profile is
  # selected. Include the init profile so the Vault endpoint and workspace path
  # are read from the same vault-init service that will perform the sync.
  compose_config="$(docker compose -f "${PROGDIR}/docker-compose.yaml" --profile init config --format json)"
  vault_addr="$(jq -r '.services["vault-init"].environment.VAULT_ADDRESS // ""' <<<"${compose_config}")"
  vault_addr="${vault_addr#"${vault_addr%%[![:space:]]*}"}"
  vault_addr="${vault_addr%"${vault_addr##*[![:space:]]}"}"
  if [ -z "$vault_addr" ]; then
    return 0
  fi

  vault_role="$(jq -r '.services["vault-init"].environment.VAULT_GCP_AUTH_ROLE // "scribe-app"' <<<"${compose_config}")"
  vault_kv_mount="$(jq -r --arg fallback "$DEFAULT_VAULT_KV_MOUNT" '.services["vault-init"].environment.VAULT_KV_MOUNT // $fallback' <<<"${compose_config}")"
  vault_secret_prefix="$(jq -r --arg fallback "$DEFAULT_VAULT_SECRET_PREFIX" '.services["vault-init"].environment.VAULT_SECRET_PREFIX // $fallback' <<<"${compose_config}")"
  vault_database_app_path="$(jq -r --arg fallback "${vault_secret_prefix}/database/app" '.services["vault-init"].environment.VAULT_DATABASE_APP_PATH // $fallback' <<<"${compose_config}")"

  echo "Syncing MariaDB Docker secret files from Vault..." >&2
  host_uid="$(id -u)"
  host_gid="$(id -g)"
  VAULT_ADDRESS="$vault_addr" \
  VAULT_GCP_AUTH_ROLE="$vault_role" \
  VAULT_KV_MOUNT="$vault_kv_mount" \
  VAULT_SECRET_PREFIX="$vault_secret_prefix" \
  VAULT_DATABASE_APP_PATH="$vault_database_app_path" \
  HOST_UID="$host_uid" \
  HOST_GID="$host_gid" \
  docker compose -f "${PROGDIR}/docker-compose.yaml" --profile init run --rm -T vault-init
}

mkdir -p "${PROGDIR}/secrets"
if SECRETS_GID="$(stat -c '%g' "${PROGDIR}/secrets" 2>/dev/null)"; then
  readonly SECRETS_GID
else
  SECRETS_GID="$(stat -f '%g' "${PROGDIR}/secrets")"
  readonly SECRETS_GID
fi

# Compose adds this host group to each non-root service that consumes a
# bind-mounted local secret. Persisting the numeric group keeps mode 0640 files
# readable without making them world-readable or requiring root-owned files.
export SCRIBE_SECRETS_GID="${SECRETS_GID}"
persist_compose_secret_group

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
normalize_secret_permissions
