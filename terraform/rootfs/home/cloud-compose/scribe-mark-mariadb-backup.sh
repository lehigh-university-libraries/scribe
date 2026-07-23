#!/usr/bin/env bash

set -Eeuo pipefail

readonly DATA_ROOT="/mnt/disks/data"
readonly BACKUP_ROOT="${MARIADB_BACKUP_ROOT:-${DATA_ROOT}/backups/mariadb}"
readonly TEST_ROOT="${SCRIBE_BACKUP_TEST_ROOT:-}"
TODAY="$(date -u +%Y%m%d)"
readonly TODAY
readonly BACKUP="$BACKUP_ROOT/primary/${TODAY}-primary.sql.gz"
readonly MARKER="$BACKUP_ROOT/.last-success"

if [[ "${SCRIBE_BACKUP_TEST_MODE:-false}" == "true" ]]; then
  [[ "$TEST_ROOT" == /* && "$TEST_ROOT" != "/" && "$TEST_ROOT" == */scribe-backup-test.* ]] || { echo "unsafe backup fixture boundary" >&2; exit 2; }
  [[ -d "$TEST_ROOT" && ! -L "$TEST_ROOT" ]] || { echo "unsafe backup fixture boundary" >&2; exit 2; }
  [[ "$BACKUP_ROOT" == "$TEST_ROOT"/* ]] || { echo "unsafe backup fixture root" >&2; exit 2; }
else
  [[ "$BACKUP_ROOT" == "${DATA_ROOT}/backups/mariadb" ]] || { echo "refusing unexpected production backup root" >&2; exit 2; }
  [[ -d "$DATA_ROOT" && ! -L "$DATA_ROOT" ]] || { echo "cloud-compose data root is unsafe" >&2; exit 1; }
  mountpoint -q -- "$DATA_ROOT" || { echo "cloud-compose data filesystem is not mounted" >&2; exit 1; }
fi
[[ -d "$BACKUP_ROOT" && ! -L "$BACKUP_ROOT" ]] || { echo "backup root is unsafe" >&2; exit 1; }
[[ -f "$BACKUP" && ! -L "$BACKUP" && -s "$BACKUP" ]] || { echo "today's primary backup is missing or unsafe" >&2; exit 1; }
[[ ( ! -e "$MARKER" && ! -L "$MARKER" ) || ( -f "$MARKER" && ! -L "$MARKER" ) ]] || { echo "backup success marker is unsafe" >&2; exit 1; }
gzip -t -- "$BACKUP"
tmp="$(mktemp "$BACKUP_ROOT/.last-success.XXXXXX")"
trap 'rm -f -- "$tmp"' EXIT
printf '%s\n' "$TODAY" >"$tmp"
chmod 0640 "$tmp"
mv -fT -- "$tmp" "$MARKER"
trap - EXIT
