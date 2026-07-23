#!/usr/bin/env bash

set -Eeuo pipefail
shopt -s nullglob

readonly RETAIN_COMPLETED="${SCRIBE_MARIADB_BACKUP_RETAIN_COMPLETED:-1}"
readonly DATA_ROOT="/mnt/disks/data"
readonly BACKUP_ROOT="${MARIADB_BACKUP_ROOT:-${DATA_ROOT}/backups/mariadb}"
readonly TEST_ROOT="${SCRIBE_BACKUP_TEST_ROOT:-}"

[[ "$RETAIN_COMPLETED" =~ ^[1-9][0-9]?$ ]] || { echo "invalid retained backup count" >&2; exit 2; }
if [[ "${SCRIBE_BACKUP_TEST_MODE:-false}" == "true" ]]; then
  [[ "$TEST_ROOT" == /* && "$TEST_ROOT" != "/" && "$TEST_ROOT" == */scribe-backup-test.* ]] || { echo "unsafe backup fixture boundary" >&2; exit 2; }
  [[ -d "$TEST_ROOT" && ! -L "$TEST_ROOT" ]] || { echo "unsafe backup fixture boundary" >&2; exit 2; }
  [[ "$BACKUP_ROOT" == "$TEST_ROOT"/* ]] || { echo "unsafe backup fixture root" >&2; exit 2; }
else
  [[ "$BACKUP_ROOT" == "${DATA_ROOT}/backups/mariadb" ]] || { echo "refusing unexpected production backup root" >&2; exit 2; }
  [[ -d "$DATA_ROOT" && ! -L "$DATA_ROOT" ]] || { echo "cloud-compose data root is unsafe" >&2; exit 1; }
  mountpoint -q -- "$DATA_ROOT" || { echo "cloud-compose data filesystem is not mounted" >&2; exit 1; }
fi

backup_parent="${BACKUP_ROOT%/*}"
[[ -d "$backup_parent" && ! -L "$backup_parent" ]] || {
  [[ ! -e "$backup_parent" && ! -L "$backup_parent" ]] || { echo "backup parent is unsafe" >&2; exit 1; }
  install -d -m 0750 -- "$backup_parent"
}
[[ -d "$BACKUP_ROOT" && ! -L "$BACKUP_ROOT" ]] || {
  [[ ! -e "$BACKUP_ROOT" && ! -L "$BACKUP_ROOT" ]] || { echo "backup root is unsafe" >&2; exit 1; }
  install -d -m 0750 -- "$BACKUP_ROOT"
}
[[ -d "$BACKUP_ROOT" && ! -L "$BACKUP_ROOT" ]] || { echo "backup root is unsafe" >&2; exit 1; }

for app_dir in "$BACKUP_ROOT"/*; do
  [[ -d "$app_dir" && ! -L "$app_dir" ]] || { echo "unexpected backup-root entry: $app_dir" >&2; exit 1; }
  app="$(basename -- "$app_dir")"
  [[ "$app" =~ ^[a-z][a-z0-9-]{0,62}$ ]] || { echo "invalid backup application directory: $app" >&2; exit 1; }

  files=()
  while IFS= read -r file; do files+=("$file"); done < <(find "$app_dir" -mindepth 1 -maxdepth 1 -printf '%f\n' | LC_ALL=C sort -r)
  for file in "${files[@]}"; do
    path="$app_dir/$file"
    [[ "$file" =~ ^[0-9]{8}-${app}\.sql\.gz$ ]] || { echo "unexpected backup entry: $path" >&2; exit 1; }
    [[ -f "$path" && ! -L "$path" && -s "$path" ]] || { echo "unsafe backup artifact: $path" >&2; exit 1; }
    date -u -d "${file:0:8}" +%Y%m%d | grep -Fxq "${file:0:8}" || { echo "invalid backup date: $path" >&2; exit 1; }
    gzip -t -- "$path"
  done

  index=0
  for file in "${files[@]}"; do
    index=$((index + 1))
    ((index <= RETAIN_COMPLETED)) && continue
    rm -- "$app_dir/$file"
  done
done
