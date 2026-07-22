#!/usr/bin/env bash

set -Eeuo pipefail

readonly BACKUP_ROOT="${MARIADB_BACKUP_ROOT:-/mnt/disks/backups/mariadb}"
TODAY="$(date -u +%Y%m%d)"
readonly TODAY
readonly BACKUP="$BACKUP_ROOT/primary/${TODAY}-primary.sql.gz"
readonly MARKER="$BACKUP_ROOT/.last-success"

[[ "$BACKUP_ROOT" == "/mnt/disks/backups/mariadb" ]] || { echo "refusing unexpected production backup root" >&2; exit 2; }
[[ -f "$BACKUP" && ! -L "$BACKUP" && -s "$BACKUP" ]] || { echo "today's primary backup is missing or unsafe" >&2; exit 1; }
gzip -t -- "$BACKUP"
tmp="$(mktemp "$BACKUP_ROOT/.last-success.XXXXXX")"
trap 'rm -f -- "$tmp"' EXIT
printf '%s\n' "$TODAY" >"$tmp"
chmod 0640 "$tmp"
mv -f -- "$tmp" "$MARKER"
trap - EXIT
