#!/bin/sh

set -eu

ROOT_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "cloud-compose snapshot contract failed: $*" >&2
  exit 1
}

module_root=""
for candidate in terraform/.terraform/modules/scribe/cloud-compose-*; do
  if [ -d "$candidate" ]; then
    module_root="$candidate"
    break
  fi
done
[ -n "$module_root" ] || fail "the pinned scribe module is not initialized; run terraform init first"

provider_main="${module_root}/providers/gcp/main.tf"
module_main="${module_root}/modules/gcp/main.tf"
module_gcp_variables="${module_root}/modules/gcp/variables.tf"
module_variables="${module_root}/variables.tf"
for required_file in "$provider_main" "$module_main" "$module_gcp_variables" "$module_variables"; do
  [ -r "$required_file" ] || fail "initialized module is missing ${required_file}"
done

scribe_module="$(sed -n '/^module "scribe" {/,/^}/p' terraform/main.tf)"
printf '%s\n' "$scribe_module" | grep -Eq 'production[[:space:]]*=[[:space:]]*local\.is_prod_workspace' ||
  fail "Scribe does not pass its production workspace decision to cloud-compose"
printf '%s\n' "$scribe_module" | grep -Eq 'enabled[[:space:]]*=[[:space:]]*var\.run_snapshots' ||
  fail "Scribe does not pass the reviewed snapshot flag to cloud-compose"
grep -Eq 'is_prod_workspace[[:space:]]*=[[:space:]]*terraform\.workspace[[:space:]]*==[[:space:]]*"prod"' terraform/main.tf ||
  fail "the production workspace predicate is not exact"
grep -Eq 'cloud_compose_machine_type[[:space:]]*=[[:space:]]*local\.is_preview_workspace[[:space:]]*\?[[:space:]]*"e2-medium"[[:space:]]*:[[:space:]]*var\.machine_type' terraform/main.tf ||
  fail "previews do not use the reviewed E2 machine profile"
grep -Eq 'cloud_compose_disk_type[[:space:]]*=[[:space:]]*local\.is_preview_workspace[[:space:]]*\?[[:space:]]*"pd-standard"[[:space:]]*:[[:space:]]*"hyperdisk-balanced"' terraform/main.tf ||
  fail "previews still consume production Hyperdisk capacity"
printf '%s\n' "$scribe_module" | grep -Eq 'machine_type[[:space:]]*=[[:space:]]*local\.cloud_compose_machine_type' ||
  fail "Scribe does not pass the workspace-specific machine profile to cloud-compose"
printf '%s\n' "$scribe_module" | grep -Eq 'type[[:space:]]*=[[:space:]]*local\.cloud_compose_disk_type' ||
  fail "Scribe does not pass the workspace-specific disk profile to cloud-compose"

grep -Eq 'production[[:space:]]*=[[:space:]]*optional\(bool,[[:space:]]*false\)' "$module_variables" ||
  fail "the initialized cloud-compose production default changed"
grep -Eq 'production[[:space:]]*=[[:space:]]*local\.gcp_instance\.production' "$provider_main" ||
  fail "cloud-compose no longer forwards instance.production to its GCP module"
grep -Eq 'run_snapshots[[:space:]]*=[[:space:]]*local\.gcp_snapshots\.enabled' "$provider_main" ||
  fail "cloud-compose no longer forwards snapshots.enabled to its GCP module"
grep -Eq 'scheduled_snapshots_enabled[[:space:]]*=[[:space:]]*var\.production[[:space:]]*&&[[:space:]]*var\.run_snapshots' "$module_main" ||
  fail "the initialized cloud-compose snapshot gate changed"

gate_consumers="$(grep -Ec '(count|for_each)[[:space:]]*=[[:space:]]*local\.scheduled_snapshots_enabled' "$module_main")"
[ "$gate_consumers" -ge 4 ] || fail "cloud-compose snapshot resources are no longer consistently gated"

grep -Eq 'data_size_gb[[:space:]]*=[[:space:]]*optional\(number,[[:space:]]*20\)' "$module_variables" ||
  fail "the initialized cloud-compose module does not expose its data-disk capacity"
grep -Eq 'data_disk_size_gb[[:space:]]*=[[:space:]]*local\.gcp_disks\.data_size_gb' "$provider_main" ||
  fail "cloud-compose no longer forwards the reviewed data-disk capacity"
grep -Eq '^variable "data_disk_size_gb"' "$module_gcp_variables" ||
  fail "the initialized cloud-compose GCP module does not accept a data-disk capacity"
grep -Eq 'size[[:space:]]*=[[:space:]]*var\.data_disk_size_gb' "$module_main" ||
  fail "cloud-compose no longer applies the reviewed data-disk capacity"

grep -Eq 'data_size_gb[[:space:]]*=[[:space:]]*local\.cloud_compose_data_disk_size_gb' terraform/main.tf ||
  fail "Scribe does not reserve logical-backup capacity on the existing data disk"
grep -Eq 'SCRIBE_MARIADB_BACKUP_MIN_FREE_BYTES[[:space:]]*=[[:space:]]*tostring\(var\.disk_size_gb[[:space:]]*\*[[:space:]]*1073741824\)' terraform/main.tf ||
  fail "Scribe does not pass a full-database free-space floor to the backup runtime"
grep -Eq 'cloud_compose_data_baseline_size_gb[[:space:]]*=[[:space:]]*20' terraform/backup.tf ||
  fail "Scribe does not preserve cloud-compose's existing data-disk baseline"
grep -Eq 'mariadb_backup_retained_completed_copies[[:space:]]*\+[[:space:]]*2' terraform/backup.tf ||
  fail "Scribe data-disk sizing no longer includes staging and safety capacity"
grep -Eq 'cloud_compose_data_disk_size_gb[[:space:]]*=[[:space:]]*local\.is_prod_workspace[[:space:]]*\?' terraform/backup.tf ||
  fail "Scribe applies the production backup capacity to non-production data disks"
grep -Eq '\)[[:space:]]*:[[:space:]]*local\.cloud_compose_data_baseline_size_gb' terraform/backup.tf ||
  fail "Scribe non-production data disks no longer retain cloud-compose's 20-GB baseline"
grep -Eq '^check "production_logical_backup_capacity"' terraform/backup.tf ||
  fail "Scribe does not enforce production logical-backup capacity"

if grep -Eq '^resource "google_compute_disk" "mariadb_backups"' terraform/backup.tf; then
  fail "Scribe still creates a dedicated logical-backup disk"
fi
if grep -Eq '^resource "google_compute_attached_disk" "mariadb_backups"' terraform/backup.tf; then
  fail "Scribe still attaches a dedicated logical-backup disk"
fi
if grep -Eq '^resource "google_compute_disk_resource_policy_attachment" "mariadb_backups_(daily|weekly)"' terraform/backup.tf; then
  fail "Scribe still maintains redundant snapshot policies for a dedicated backup disk"
fi
grep -Eq 'from[[:space:]]*=[[:space:]]*google_compute_disk\.mariadb_backups' terraform/backup.tf ||
  fail "Scribe does not forget the retired data-bearing backup disk"
removed_backup_disk="$(sed -n '/^removed {/,/^}/p' terraform/backup.tf)"
printf '%s\n' "$removed_backup_disk" | grep -Eq '^[[:space:]]+destroy[[:space:]]*=[[:space:]]*false[[:space:]]*$' ||
  fail "Scribe could destroy the retired backup disk and its historical dumps"
[ ! -e terraform/rootfs/home/cloud-compose/configure-scribe-backup-disk.sh ] ||
  fail "Scribe still configures the retired dedicated backup disk"

printf '%s\n' "$scribe_module" | grep -Eq 'extra_env[[:space:]]*=[[:space:]]*local\.is_prod_workspace[[:space:]]*\?' ||
  fail "Scribe applies the production backup free-space floor outside production"
printf '%s\n' "$scribe_module" | grep -Fq 'systemctl disable --now cloud-compose-mariadb-backup.timer cloud-compose-mariadb-backup.service' ||
  fail "Scribe does not keep the production-only logical backup timer disabled in non-production"

backup_dropin=terraform/rootfs/etc/systemd/system/cloud-compose-mariadb-backup.service.d/scribe-backups.conf
grep -Fq 'RequiresMountsFor=/mnt/disks/data' "$backup_dropin" ||
  fail "the MariaDB backup service does not require cloud-compose's data disk"
grep -Fq 'Environment=MARIADB_BACKUP_ROOT=/mnt/disks/data/backups/mariadb' "$backup_dropin" ||
  fail "the MariaDB backup service does not write logical dumps to the data disk"
grep -Fq 'ExecStartPre=/bin/bash /home/cloud-compose/scribe-check-mariadb-backup-capacity.sh' "$backup_dropin" ||
  fail "the MariaDB backup service does not enforce staging capacity before backup"
# shellcheck disable=SC2016 # The script contract must contain this literal variable reference.
grep -Fq 'mountpoint -q -- "$DATA_ROOT"' terraform/rootfs/home/cloud-compose/scribe-prune-mariadb-backups.sh ||
  fail "retention can run without proving that the data disk is mounted"

fixture_dir="$(mktemp -d "${TMPDIR:-/tmp}/scribe-snapshot-contract.XXXXXX")"
trap 'rm -rf "$fixture_dir"' EXIT
cat >"$fixture_dir/main.tf" <<'HCL'
resource "terraform_data" "disk" {
  input = "historical-logical-backups"
}

resource "terraform_data" "attachment" {
  input = terraform_data.disk.output
}
HCL
terraform -chdir="$fixture_dir" init -backend=false -input=false >/dev/null
terraform -chdir="$fixture_dir" apply -input=false -auto-approve >/dev/null

cat >"$fixture_dir/main.tf" <<'HCL'
removed {
  from = terraform_data.disk

  lifecycle {
    destroy = false
  }
}
HCL
terraform -chdir="$fixture_dir" plan -input=false -out=retirement.tfplan >/dev/null
# The pinned HashiCorp Terraform image intentionally contains only Terraform
# and BusyBox. Inspect its pinned, no-color plan rendering so this contract
# follows the same path with either a local binary or the container fallback.
plan_text="$fixture_dir/retirement.txt"
terraform -chdir="$fixture_dir" show -no-color retirement.tfplan >"$plan_text"
grep -Fq '# terraform_data.disk will no longer be managed by Terraform, but will not be destroyed' "$plan_text" ||
  fail "Terraform did not forget the retired data-bearing disk"
grep -Fq '# terraform_data.attachment will be destroyed' "$plan_text" ||
  fail "Terraform did not retire the old non-data-bearing attachment"

echo "cloud-compose production snapshot contracts passed."
