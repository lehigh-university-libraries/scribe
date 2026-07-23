#!/usr/bin/env bash

set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fixture="$(mktemp -d "${TMPDIR:-/tmp}/scribe-backup-test.XXXXXX")"
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
    SCRIBE_BACKUP_TEST_ROOT="$fixture" \
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

if SCRIBE_BACKUP_TEST_MODE=true SCRIBE_BACKUP_TEST_ROOT="$fixture" MARIADB_BACKUP_ROOT=/tmp/not-a-scribe-fixture \
  bash "$ROOT_DIR/terraform/rootfs/home/cloud-compose/scribe-prune-mariadb-backups.sh" >/dev/null 2>&1; then
  echo "retention accepted an unsafe fixture root" >&2
  exit 1
fi

empty_root="$fixture/empty/mariadb"
SCRIBE_BACKUP_TEST_MODE=true \
  SCRIBE_BACKUP_TEST_ROOT="$fixture" \
  MARIADB_BACKUP_ROOT="$empty_root" \
  SCRIBE_MARIADB_BACKUP_RETAIN_COMPLETED=1 \
  bash "$ROOT_DIR/terraform/rootfs/home/cloud-compose/scribe-prune-mariadb-backups.sh"
[[ -d "$empty_root" && ! -L "$empty_root" ]]

marker_root="$fixture/marker/mariadb"
marker_app_dir="$marker_root/primary"
mkdir -p "$marker_app_dir"
today="$(date -u +%Y%m%d)"
# shellcheck disable=SC2016 # Backticks are literal SQL identifier delimiters.
printf 'CREATE TABLE `annotation_pages` (`id` bigint);\n' | gzip -n >"$marker_app_dir/${today}-primary.sql.gz"
SCRIBE_BACKUP_TEST_MODE=true \
  SCRIBE_BACKUP_TEST_ROOT="$fixture" \
  MARIADB_BACKUP_ROOT="$marker_root" \
  bash "$ROOT_DIR/terraform/rootfs/home/cloud-compose/scribe-mark-mariadb-backup.sh"
[[ "$(cat "$marker_root/.last-success")" == "$today" ]]
[[ "$(stat -c '%a' "$marker_root/.last-success")" == "640" ]]

rm -- "$marker_root/.last-success"
mkdir "$marker_root/.last-success"
if SCRIBE_BACKUP_TEST_MODE=true \
  SCRIBE_BACKUP_TEST_ROOT="$fixture" \
  MARIADB_BACKUP_ROOT="$marker_root" \
  bash "$ROOT_DIR/terraform/rootfs/home/cloud-compose/scribe-mark-mariadb-backup.sh" >/dev/null 2>&1; then
  echo "backup marker accepted an unsafe destination" >&2
  exit 1
fi
rmdir -- "$marker_root/.last-success"

capacity_data_root="$fixture/capacity/data"
capacity_env="$fixture/capacity/application-env.json"
mkdir -p "$capacity_data_root"
printf '%s\n' '{"SCRIBE_MARIADB_BACKUP_MIN_FREE_BYTES":"1073741824"}' >"$capacity_env"
SCRIBE_BACKUP_TEST_MODE=true \
  SCRIBE_BACKUP_TEST_ROOT="$fixture" \
  SCRIBE_BACKUP_TEST_DATA_ROOT="$capacity_data_root" \
  SCRIBE_BACKUP_TEST_AVAILABLE_BYTES=1073741824 \
  CLOUD_COMPOSE_APPLICATION_ENV_FILE="$capacity_env" \
  bash "$ROOT_DIR/terraform/rootfs/home/cloud-compose/scribe-check-mariadb-backup-capacity.sh"

if SCRIBE_BACKUP_TEST_MODE=true \
  SCRIBE_BACKUP_TEST_ROOT="$fixture" \
  SCRIBE_BACKUP_TEST_DATA_ROOT="$capacity_data_root" \
  SCRIBE_BACKUP_TEST_AVAILABLE_BYTES=1073741823 \
  CLOUD_COMPOSE_APPLICATION_ENV_FILE="$capacity_env" \
  bash "$ROOT_DIR/terraform/rootfs/home/cloud-compose/scribe-check-mariadb-backup-capacity.sh" >/dev/null 2>&1; then
  echo "backup capacity preflight accepted insufficient free space" >&2
  exit 1
fi

printf '%s\n' '{"SCRIBE_MARIADB_BACKUP_MIN_FREE_BYTES":"invalid"}' >"$capacity_env"
if SCRIBE_BACKUP_TEST_MODE=true \
  SCRIBE_BACKUP_TEST_ROOT="$fixture" \
  SCRIBE_BACKUP_TEST_DATA_ROOT="$capacity_data_root" \
  SCRIBE_BACKUP_TEST_AVAILABLE_BYTES=1073741824 \
  CLOUD_COMPOSE_APPLICATION_ENV_FILE="$capacity_env" \
  bash "$ROOT_DIR/terraform/rootfs/home/cloud-compose/scribe-check-mariadb-backup-capacity.sh" >/dev/null 2>&1; then
  echo "backup capacity preflight accepted an invalid minimum" >&2
  exit 1
fi

printf '%s\n' '{"SCRIBE_MARIADB_BACKUP_MIN_FREE_BYTES":"1073741824\n"}' >"$capacity_env"
if SCRIBE_BACKUP_TEST_MODE=true \
  SCRIBE_BACKUP_TEST_ROOT="$fixture" \
  SCRIBE_BACKUP_TEST_DATA_ROOT="$capacity_data_root" \
  SCRIBE_BACKUP_TEST_AVAILABLE_BYTES=1073741824 \
  CLOUD_COMPOSE_APPLICATION_ENV_FILE="$capacity_env" \
  bash "$ROOT_DIR/terraform/rootfs/home/cloud-compose/scribe-check-mariadb-backup-capacity.sh" >/dev/null 2>&1; then
  echo "backup capacity preflight accepted a newline-suffixed minimum" >&2
  exit 1
fi

echo "MariaDB backup retention fixtures passed."
