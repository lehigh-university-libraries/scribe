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
