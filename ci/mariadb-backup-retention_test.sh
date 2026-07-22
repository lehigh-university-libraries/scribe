#!/usr/bin/env bash

set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fixture="$(mktemp -d /tmp/scribe-backup-test.XXXXXX)"
trap 'rm -rf -- "$fixture"' EXIT
backup_root="$fixture/backups"
app_dir="$backup_root/primary"
mkdir -p "$app_dir"

write_backup() {
  local date_value="$1"
  # shellcheck disable=SC2016 # Backticks are literal SQL identifier delimiters.
  printf 'CREATE TABLE `annotation_pages` (`id` bigint);\n' | gzip -n >"$app_dir/${date_value}-primary.sql.gz"
}

run_prune() {
  SCRIBE_BACKUP_TEST_MODE=true \
    MARIADB_BACKUP_ROOT="$backup_root" \
    SCRIBE_MARIADB_BACKUP_RETAIN_COMPLETED=1 \
    bash "$ROOT_DIR/terraform/rootfs/home/cloud-compose/scribe-prune-mariadb-backups.sh"
}

write_backup 20260719
write_backup 20260720
write_backup 20260721
run_prune
[[ -f "$app_dir/20260721-primary.sql.gz" ]]
[[ ! -e "$app_dir/20260720-primary.sql.gz" && ! -e "$app_dir/20260719-primary.sql.gz" ]]

write_backup 20260720
ln -s -- 20260721-primary.sql.gz "$app_dir/20260722-primary.sql.gz"
if run_prune >/dev/null 2>&1; then
  echo "retention accepted a symlinked backup" >&2
  exit 1
fi
[[ -f "$app_dir/20260720-primary.sql.gz" ]]
rm -- "$app_dir/20260722-primary.sql.gz"

mv -- "$app_dir/20260720-primary.sql.gz" "$app_dir/20261340-primary.sql.gz"
if run_prune >/dev/null 2>&1; then
  echo "retention accepted an invalid calendar date" >&2
  exit 1
fi
[[ -f "$app_dir/20261340-primary.sql.gz" ]]
rm -- "$app_dir/20261340-primary.sql.gz"

mkdir "$app_dir/.stale.staging"
if run_prune >/dev/null 2>&1; then
  echo "retention accepted an unexpected staging directory" >&2
  exit 1
fi
rm -r -- "$app_dir/.stale.staging"

if SCRIBE_BACKUP_TEST_MODE=true MARIADB_BACKUP_ROOT=/tmp/not-a-scribe-fixture \
  bash "$ROOT_DIR/terraform/rootfs/home/cloud-compose/scribe-prune-mariadb-backups.sh" >/dev/null 2>&1; then
  echo "retention accepted an unsafe fixture root" >&2
  exit 1
fi

echo "MariaDB backup retention fixtures passed."
