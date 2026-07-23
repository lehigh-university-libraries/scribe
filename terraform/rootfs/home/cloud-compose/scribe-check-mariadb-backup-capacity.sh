#!/usr/bin/env bash

set -Eeuo pipefail

readonly PRODUCTION_DATA_ROOT="/mnt/disks/data"
readonly APPLICATION_ENV_FILE="${CLOUD_COMPOSE_APPLICATION_ENV_FILE:-/home/cloud-compose/application-env.json}"
readonly TEST_ROOT="${SCRIBE_BACKUP_TEST_ROOT:-}"

if [[ "${SCRIBE_BACKUP_TEST_MODE:-false}" == "true" ]]; then
  [[ "$TEST_ROOT" == /* && "$TEST_ROOT" != "/" && "$TEST_ROOT" == */scribe-backup-test.* ]] || { echo "unsafe backup fixture boundary" >&2; exit 2; }
  [[ -d "$TEST_ROOT" && ! -L "$TEST_ROOT" ]] || { echo "unsafe backup fixture boundary" >&2; exit 2; }
  data_root="${SCRIBE_BACKUP_TEST_DATA_ROOT:-}"
  [[ "$data_root" == "$TEST_ROOT"/* && -d "$data_root" && ! -L "$data_root" ]] || { echo "unsafe backup fixture data root" >&2; exit 2; }
  [[ "$APPLICATION_ENV_FILE" == "$TEST_ROOT"/* ]] || { echo "unsafe backup fixture application environment" >&2; exit 2; }
  available_bytes="${SCRIBE_BACKUP_TEST_AVAILABLE_BYTES:-}"
else
  data_root="$PRODUCTION_DATA_ROOT"
  [[ -d "$data_root" && ! -L "$data_root" ]] || { echo "cloud-compose data root is unsafe" >&2; exit 1; }
  mountpoint -q -- "$data_root" || { echo "cloud-compose data filesystem is not mounted" >&2; exit 1; }
  [[ "$APPLICATION_ENV_FILE" == "/home/cloud-compose/application-env.json" ]] || { echo "refusing unexpected application environment path" >&2; exit 2; }
  available_bytes="$(df -B1 --output=avail -- "$data_root" | awk 'NR == 2 { print $1 }')"
fi

[[ -f "$APPLICATION_ENV_FILE" && ! -L "$APPLICATION_ENV_FILE" ]] || { echo "application environment data is unsafe" >&2; exit 1; }
minimum_free_bytes="$(jq -er '
  .SCRIBE_MARIADB_BACKUP_MIN_FREE_BYTES
  | select(type == "string" and length > 0)
  | . as $value
  | explode as $chars
  | select(
      ($chars[0] >= 49 and $chars[0] <= 57) and
      all($chars[]; . >= 48 and . <= 57)
    )
  | $value
' "$APPLICATION_ENV_FILE")" || { echo "minimum backup capacity is missing or invalid" >&2; exit 1; }
[[ "$minimum_free_bytes" =~ ^[1-9][0-9]*$ ]] || { echo "minimum backup capacity is invalid" >&2; exit 2; }
[[ "$available_bytes" =~ ^(0|[1-9][0-9]*)$ ]] || { echo "available backup capacity is invalid" >&2; exit 1; }
((available_bytes >= minimum_free_bytes)) || {
  echo "cloud-compose data disk has ${available_bytes} bytes free; MariaDB backup requires at least ${minimum_free_bytes}" >&2
  exit 1
}
