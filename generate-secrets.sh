#!/usr/bin/env bash
# shellcheck shell=bash

set -euf -o pipefail
umask 077

PROGDIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readonly PROGDIR

# Generate missing Docker Compose secret files without rotating valid ones, and
# converge interrupted short writes for the three application-owned tokens.
readonly CHARACTERS='[A-Za-z0-9]'
readonly LENGTH=32
readonly EXTERNALLY_MANAGED_SECRETS='(^|/)GOOGLE_APPLICATION_CREDENTIALS$'
readonly REGENERATABLE_LOCAL_TOKENS='(^|/)(page_token_signing_key|triplet_presentation_write_token|triplet_source_read_token)$'
readonly EXTERNALLY_MANAGED_SECRET_PLACEHOLDER='{}'
readonly DEFAULT_VAULT_KV_MOUNT='secret'
readonly DEFAULT_VAULT_SECRET_PREFIX='scribe/dev'
readonly REPAIR_LOCAL_TOKENS="${SCRIBE_REPAIR_LOCAL_TOKENS:-false}"

case "$REPAIR_LOCAL_TOKENS" in
  true | false) ;;
  *)
    echo "SCRIBE_REPAIR_LOCAL_TOKENS must be true or false." >&2
    exit 2
    ;;
esac

trace_app_init_stage() {
  case "${1:-}" in
    secrets-script-start | \
      secrets-directory-ready | \
      secrets-group-ready | \
      secrets-env-ready | \
      secrets-compose-list-start | \
      secrets-compose-list-ready | \
      secrets-files-ready | \
      secrets-vault-config-start | \
      secrets-vault-config-ready | \
      secrets-vault-settings-ready | \
      secrets-vault-run-start | \
      secrets-vault-run-failed | \
      secrets-vault-run-complete | \
      secrets-vault-sync-complete | \
      secrets-permissions-start | \
      secrets-permissions-complete)
      printf 'SCRIBE_APP_INIT_TRACE_V1 stage=%s\n' "$1" || :
      ;;
    *) return 1 ;;
  esac
}

validate_secret_path() {
  local path="$1"
  local secrets_dir="${PROGDIR}/secrets"
  local links

  [[ "$(dirname -- "$path")" == "$secrets_dir" ]] || {
    echo "Refusing Compose secret outside ${secrets_dir}: ${path}" >&2
    return 1
  }
  if [[ ! -e "$path" && ! -L "$path" ]]; then
    return 0
  fi
  [[ ! -L "$path" && -f "$path" ]] || {
    echo "Refusing linked or non-regular Compose secret: ${path}" >&2
    return 1
  }
  links="$(stat -c '%h' "$path" 2>/dev/null || stat -f '%l' "$path")"
  [[ "$links" == 1 ]] || {
    echo "Refusing hard-linked Compose secret: ${path}" >&2
    return 1
  }
}

local_token_effective_size() {
  LC_ALL=C tr -d '\r\n' <"$1" | wc -c | tr -d '[:space:]'
}

create_file_without_overwrite() {
  local target="$1" kind="$2" temp_file

  temp_file="$(mktemp "${PROGDIR}/secrets/.scribe-secret.XXXXXX")"
  case "$kind" in
    placeholder)
      printf '%s' "$EXTERNALLY_MANAGED_SECRET_PLACEHOLDER" >"$temp_file"
      ;;
    random)
      (grep -ao "${CHARACTERS}" </dev/urandom || true) |
        head "-${LENGTH}" |
        tr -d '\n' >"$temp_file"
      [[ "$(wc -c <"$temp_file")" -eq "$LENGTH" ]] || {
        rm -f -- "$temp_file"
        echo "Could not generate a complete secret." >&2
        return 1
      }
      ;;
    *)
      rm -f -- "$temp_file"
      return 2
      ;;
  esac
  chmod 600 "$temp_file"
  if ! ln -- "$temp_file" "$target"; then
    rm -f -- "$temp_file"
    echo "Refusing to overwrite a secret created concurrently." >&2
    return 1
  fi
  rm -f -- "$temp_file"
}

require_linux_in_place_token_repair() {
  local kernel

  kernel="$(uname -s 2>/dev/null)" || kernel=unknown
  if [[ "$kernel" == Linux && -d /proc/self/fd ]]; then
    return 0
  fi

  echo "In-place application-token repair is supported only on Linux with /proc: ${1}" >&2
  echo "Run 'make down', remove only the reported short token file, then run 'make up' to regenerate it." >&2
  return 1
}

repair_short_secret_in_place() (
  local target="$1"
  local before_metadata before_effective_size before_links fd_effective_size fd_metadata after_metadata value
  local secret_fd fd_path

  before_metadata="$(
    stat -L -c '%d:%i:%h:%s' "$target" 2>/dev/null ||
      stat -L -f '%d:%i:%l:%z' "$target"
  )" || return 1
  before_links="${before_metadata%:*}"
  before_links="${before_links##*:}"
  before_effective_size="$(local_token_effective_size "$target")"
  [[ "$before_links" == 1 &&
    "$before_effective_size" =~ ^[0-9]+$ &&
    "$before_effective_size" -lt "$LENGTH" &&
    ! -L "$target" &&
    -f "$target" ]] || {
    echo "Refusing to repair a changed or sufficiently long secret: ${target}" >&2
    return 1
  }

  exec {secret_fd}<>"$target" || return 1
  fd_path="/proc/self/fd/${secret_fd}"
  if command -v flock >/dev/null 2>&1; then
    flock -n "$secret_fd" || {
      echo "Secret repair is already running: ${target}" >&2
      return 1
    }
  fi
  if ! fd_metadata="$(
    stat -L -c '%d:%i:%h:%s' "$fd_path" 2>/dev/null ||
      stat -L -f '%d:%i:%l:%z' "$fd_path"
  )"; then
    echo "Could not verify the secret descriptor before repair." >&2
    return 1
  fi
  if ! after_metadata="$(
    stat -L -c '%d:%i:%h:%s' "$target" 2>/dev/null ||
      stat -L -f '%d:%i:%l:%z' "$target"
  )"; then
    echo "Could not verify the secret path before repair." >&2
    return 1
  fi
  [[ "$fd_metadata" == "$before_metadata" &&
    "$after_metadata" == "$before_metadata" &&
    ! -L "$target" ]] || {
    echo "Secret path changed while opening it for repair: ${target}" >&2
    return 1
  }
  fd_effective_size="$(local_token_effective_size "$fd_path")"
  [[ "$fd_effective_size" =~ ^[0-9]+$ ]] || {
    echo "Could not verify the effective application-token length." >&2
    return 1
  }
  if [[ "$fd_effective_size" -ge "$LENGTH" ]]; then
    echo "Application token became valid while waiting for its repair lock; preserving it." >&2
    return 0
  fi

  value="$(
    (grep -ao "${CHARACTERS}" </dev/urandom || true) |
      head "-${LENGTH}" |
      tr -d '\n'
  )"
  [[ "${#value}" -eq "$LENGTH" ]] || {
    echo "Could not generate a complete replacement secret." >&2
    return 1
  }
  # Truncate through the verified descriptor, never through the mutable source
  # path. If publication or its verification fails, leave a zero-byte file so
  # the next lifecycle can retry instead of accepting a partial token.
  if ! : >"$fd_path" || ! printf '%s' "$value" >&"$secret_fd"; then
    : >"$fd_path" || :
    echo "Could not publish a complete replacement secret." >&2
    return 1
  fi

  if ! fd_metadata="$(
    stat -L -c '%d:%i:%h:%s' "$fd_path" 2>/dev/null ||
      stat -L -f '%d:%i:%l:%z' "$fd_path"
  )"; then
    : >"$fd_path" || :
    echo "Could not verify the replacement secret descriptor." >&2
    return 1
  fi
  if ! after_metadata="$(
    stat -L -c '%d:%i:%h:%s' "$target" 2>/dev/null ||
      stat -L -f '%d:%i:%l:%z' "$target"
  )"; then
    : >"$fd_path" || :
    echo "Could not verify the replacement secret path." >&2
    return 1
  fi
  [[ "$fd_metadata" == "${before_metadata%:*}:${LENGTH}" &&
    "$after_metadata" == "$fd_metadata" &&
    ! -L "$target" ]] || {
    : >"$fd_path" || :
    echo "Secret path changed while publishing its repair: ${target}" >&2
    return 1
  }
)

normalize_secret_permissions() {
  local secrets_dir="${PROGDIR}/secrets"
  local effective_size secret

  # Cloud Compose installs the application credential as a root-managed file
  # before this unprivileged lifecycle runs. Only files created or rewritten
  # by this script belong to its permission-normalization boundary.
  chmod 750 "${secrets_dir}"
  for secret in "${LOCALLY_MANAGED_SECRETS[@]}"; do
    if ! validate_secret_path "$secret" || [[ ! -f "$secret" ]]; then
      echo "Refusing unsafe locally managed secret path: ${secret}" >&2
      return 1
    fi
    if [[ "$secret" =~ $REGENERATABLE_LOCAL_TOKENS ]]; then
      effective_size="$(local_token_effective_size "$secret")"
      [[ "$effective_size" =~ ^[0-9]+$ &&
        "$effective_size" -ge "$LENGTH" ]] || {
        echo "Locally managed application token remains short after convergence: ${secret}" >&2
        return 1
      }
    else
      [[ -s "$secret" ]] || {
        echo "Locally managed secret remains empty after convergence: ${secret}" >&2
        return 1
      }
    fi
    if command -v chgrp >/dev/null 2>&1; then
      chgrp "${SECRETS_GID}" "${secret}"
    fi
    chmod 640 "${secret}"
  done
}

persist_compose_env_value() {
  local name="$1" value="$2"
  local env_file="${PROGDIR}/.env"
  local temp_file

  [[ "$name" =~ ^[A-Z_][A-Z0-9_]*$ &&
    "$value" != *$'\n'* &&
    "$value" != *$'\r'* ]] || return 2
  if [[ -e "$env_file" || -L "$env_file" ]]; then
    [[ ! -L "$env_file" && -f "$env_file" ]] || {
      echo "Refusing unsafe Compose environment file: ${env_file}" >&2
      return 1
    }
  fi
  temp_file="$(mktemp "${PROGDIR}/.env.XXXXXX")"
  if [[ -f "$env_file" ]]; then
    if ! awk -v name="$name" -v value="$value" '
      index($0, name "=") != 1 { print }
      END { print name "=" value }
    ' "${env_file}" > "${temp_file}"; then
      rm -f "${temp_file}"
      return 1
    fi
  else
    printf '%s=%s\n' "$name" "$value" >"$temp_file"
  fi
  chmod 600 "${temp_file}"
  mv -f "${temp_file}" "${env_file}"
}

persist_compose_secret_group() {
  persist_compose_env_value SCRIBE_SECRETS_GID "$SCRIBE_SECRETS_GID"
}

persist_local_token_generation() {
  local value

  value="$(
    (grep -ao "${CHARACTERS}" </dev/urandom || true) |
      head "-${LENGTH}" |
      tr -d '\n'
  )"
  [[ "${#value}" -eq "$LENGTH" ]] || {
    echo "Could not generate a complete local-token generation." >&2
    return 1
  }
  persist_compose_env_value SCRIBE_LOCAL_TOKEN_GENERATION "$value"
}

ensure_local_token_generation() {
  if [[ "$local_token_generation_persisted" == false ]]; then
    persist_local_token_generation
    local_token_generation_persisted=true
  fi
}

sync_database_secrets_from_vault() {
  local compose_config vault_addr vault_role vault_kv_mount vault_secret_prefix vault_database_app_path host_uid host_gid vault_run_status

  # Compose excludes profiled services from `config` unless the profile is
  # selected. Include the init profile so the Vault endpoint and workspace path
  # are read from the same vault-init service that will perform the sync.
  trace_app_init_stage secrets-vault-config-start
  compose_config="$(docker compose -f "${PROGDIR}/docker-compose.yaml" --profile init config --format json)"
  trace_app_init_stage secrets-vault-config-ready
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
  trace_app_init_stage secrets-vault-settings-ready

  echo "Syncing MariaDB Docker secret files from Vault..." >&2
  host_uid="$(id -u)"
  host_gid="$(id -g)"
  trace_app_init_stage secrets-vault-run-start
  if VAULT_ADDRESS="$vault_addr" \
    VAULT_GCP_AUTH_ROLE="$vault_role" \
    VAULT_KV_MOUNT="$vault_kv_mount" \
    VAULT_SECRET_PREFIX="$vault_secret_prefix" \
    VAULT_DATABASE_APP_PATH="$vault_database_app_path" \
    HOST_UID="$host_uid" \
    HOST_GID="$host_gid" \
      docker compose -f "${PROGDIR}/docker-compose.yaml" --profile init run --rm -T vault-init; then
    :
  else
    vault_run_status=$?
    trace_app_init_stage secrets-vault-run-failed
    return "$vault_run_status"
  fi
  trace_app_init_stage secrets-vault-run-complete
}

declare compose_secret_files existing_token_consumers secret_size
declare -a SECRETS=()
declare -a LOCALLY_MANAGED_SECRETS=()
local_token_generation_persisted=false

trace_app_init_stage secrets-script-start
if [[ -e "${PROGDIR}/secrets" || -L "${PROGDIR}/secrets" ]]; then
  [[ ! -L "${PROGDIR}/secrets" && -d "${PROGDIR}/secrets" ]] || {
    echo "Compose secrets directory is unsafe." >&2
    exit 1
  }
else
  mkdir "${PROGDIR}/secrets"
fi
trace_app_init_stage secrets-directory-ready
if SECRETS_GID="$(stat -c '%g' "${PROGDIR}/secrets" 2>/dev/null)"; then
  readonly SECRETS_GID
else
  SECRETS_GID="$(stat -f '%g' "${PROGDIR}/secrets")"
  readonly SECRETS_GID
fi
trace_app_init_stage secrets-group-ready

# Compose adds this host group to each non-root service that consumes a
# bind-mounted local secret. Persisting the numeric group keeps mode 0640 files
# readable without making them world-readable or requiring root-owned files.
export SCRIBE_SECRETS_GID="${SECRETS_GID}"
persist_compose_secret_group
trace_app_init_stage secrets-env-ready

trace_app_init_stage secrets-compose-list-start
compose_secret_files="$(
  docker compose -f "${PROGDIR}/docker-compose.yaml" config --format json |
    jq -er '
      (.secrets // {})
      | [to_entries[].value.file]
      | if all(.[]; type == "string" and length > 0) then unique[] else error("invalid Compose secret path") end
    '
)"
if [[ -n "$compose_secret_files" ]]; then
  mapfile -t SECRETS <<<"$compose_secret_files"
fi
trace_app_init_stage secrets-compose-list-ready

for secret in "${SECRETS[@]}"; do
  validate_secret_path "$secret"
  if [[ "${secret}" =~ ${EXTERNALLY_MANAGED_SECRETS} ]]; then
    if [[ ! -e "$secret" && ! -L "$secret" ]]; then
      echo "Creating placeholder for externally managed secret: ${secret}" >&2
      create_file_without_overwrite "$secret" placeholder
      validate_secret_path "$secret"
      LOCALLY_MANAGED_SECRETS+=("${secret}")
    fi
    continue
  fi
  LOCALLY_MANAGED_SECRETS+=("${secret}")
  if [[ -f "$secret" &&
    "$secret" =~ $REGENERATABLE_LOCAL_TOKENS ]]; then
    secret_size="$(local_token_effective_size "$secret")"
  else
    secret_size="$LENGTH"
  fi
  if [[ -f "$secret" &&
    "$secret_size" =~ ^[0-9]+$ &&
    "$secret_size" -lt "$LENGTH" &&
    "$secret" =~ $REGENERATABLE_LOCAL_TOKENS ]]; then
    [[ "$REPAIR_LOCAL_TOKENS" == true ]] || {
      echo "Short local application token requires the full Compose up lifecycle: ${secret}" >&2
      exit 1
    }
    require_linux_in_place_token_repair "$secret" || exit 1
    # The cloud-compose lifecycle lock serializes this application-owned
    # repair. Change a nonsecret Compose label before touching the token so the
    # next ordinary `docker compose up` must recreate every file-to-environment
    # consumer, even if this lifecycle is interrupted and later retried.
    ensure_local_token_generation
    echo "Repairing short locally generated secret: ${secret}" >&2
    repair_short_secret_in_place "$secret"
  fi
  if [[ ! -e "$secret" && ! -L "$secret" ]]; then
    if [[ "$secret" =~ $REGENERATABLE_LOCAL_TOKENS ]]; then
      if [[ "$REPAIR_LOCAL_TOKENS" == true ]]; then
        ensure_local_token_generation
      else
        existing_token_consumers="$(
          docker compose -f "${PROGDIR}/docker-compose.yaml" \
            ps --all --quiet api worker triplet
        )" || {
          echo "Could not verify existing application-token consumers." >&2
          exit 1
        }
        [[ -z "$existing_token_consumers" ]] || {
          echo "Missing local application token requires the full Compose up lifecycle: ${secret}" >&2
          exit 1
        }
      fi
    fi
    echo "Creating: ${secret}" >&2
    create_file_without_overwrite "$secret" random
    validate_secret_path "$secret"
  fi
done
trace_app_init_stage secrets-files-ready

sync_database_secrets_from_vault
trace_app_init_stage secrets-vault-sync-complete
trace_app_init_stage secrets-permissions-start
normalize_secret_permissions
trace_app_init_stage secrets-permissions-complete
