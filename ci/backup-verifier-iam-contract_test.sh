#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "Backup verifier IAM contract failed: $*" >&2
  exit 1
}

transfer_identity_block="$(sed -n '/^resource "google_project_service_identity" "storage_transfer" {/,/^}/p' terraform/backup.tf)"
rg -q 'provider[[:space:]]*=[[:space:]]*google-beta' <<<"$transfer_identity_block" ||
  fail "Storage Transfer service-agent creation does not use the pinned beta provider"
rg -q 'service[[:space:]]*=[[:space:]]*"storagetransfer.googleapis.com"' <<<"$transfer_identity_block" ||
  fail "Storage Transfer service-agent creation targets the wrong API"
rg -q 'depends_on[[:space:]]*=[[:space:]]*\[google_project_service.storage_transfer\]' <<<"$transfer_identity_block" ||
  fail "Storage Transfer service-agent creation can race API enablement"
rg -Uq '(?s)data "google_storage_transfer_project_service_account" "backup" \{.*?depends_on[[:space:]]*=[[:space:]]*\[google_project_service_identity\.storage_transfer\]' terraform/backup.tf ||
  fail "Storage Transfer service-agent lookup can race explicit identity creation"

role_block="$(sed -n '/^resource "google_project_iam_custom_role" "backup_restore_verifier" {/,/^}/p' terraform/backup.tf)"
actual_permissions="$(rg -o '"(compute|resourcemanager)\.[A-Za-z0-9.]+' <<<"$role_block" | tr -d '"' | sort -u)"
expected_permissions=$'compute.disks.create\ncompute.disks.delete\ncompute.disks.get\ncompute.disks.list\ncompute.disks.setLabels\ncompute.disks.useReadOnly\ncompute.firewalls.get\ncompute.instances.create\ncompute.instances.delete\ncompute.instances.get\ncompute.instances.getSerialPortOutput\ncompute.instances.list\ncompute.instances.setLabels\ncompute.instances.setMetadata\ncompute.instances.setTags\ncompute.networks.use\ncompute.projects.get\ncompute.snapshots.list\ncompute.snapshots.useReadOnly\ncompute.subnetworks.use\ncompute.zoneOperations.get'
[[ "$actual_permissions" == "$expected_permissions" ]] || {
  printf 'Unexpected backup verifier permissions:\n%s\n' "$actual_permissions" >&2
  fail "custom role is not the reviewed disposable-restore permission set"
}

rg -Uq '(?s)resource "google_project_iam_member" "backup_restore_verifier" \{.*?role[[:space:]]*=[[:space:]]*google_project_iam_custom_role\.backup_restore_verifier\[0\]\.name' terraform/backup.tf ||
  fail "dedicated identity does not receive the custom restore role"
rg -Uq '(?s)resource "google_project_iam_member" "backup_transfer_viewer" \{.*?role[[:space:]]*=[[:space:]]*"roles/storagetransfer.viewer"' terraform/backup.tf ||
  fail "dedicated identity cannot observe transfer freshness"
for resource in backup_verifier_state_objects backup_verifier_upload_objects; do
  block="$(sed -n "/^resource \"google_storage_bucket_iam_member\" \"${resource}\" {/,/^}/p" terraform/backup.tf)"
  rg -q 'role[[:space:]]*=[[:space:]]*"roles/storage\.objectViewer"' <<<"$block" ||
    fail "${resource} is not the reviewed object-read grant"
  rg -q 'member[[:space:]]*=[[:space:]]*"serviceAccount:\$\{var\.backup_restore_service_account_email\}"' <<<"$block" ||
    fail "${resource} is not assigned to the backup verifier"
done
rg -Uq '(?s)backup_verifier_state_objects.*?bucket[[:space:]]*=[[:space:]]*local\.terraform_state_bucket' terraform/backup.tf ||
  fail "state object read grant is missing"
rg -Uq '(?s)backup_verifier_upload_objects.*?bucket[[:space:]]*=[[:space:]]*google_storage_bucket\.uploads_backup\[0\]\.name' terraform/backup.tf ||
  fail "independent upload backup read grant is missing"

if rg -Uq '(?s)backup_restore_service_account_email.*?roles/(owner|editor|storage\.admin|compute\.admin|cloudkms\.|iam\.|run\.|secretmanager\.)' terraform/*.tf; then
  fail "backup verifier received a broad runtime, KMS, or IAM role"
fi
rg -Uq '(?s)production_backup_restore_identity.*?backup_restore_service_account_email != module\.scribe\.appGsa\.email.*?backup_restore_service_account_email != module\.scribe\.instance\.gsa\.email.*?backup_restore_service_account_email != local\.vault_gsa.*?backup_restore_service_account_email != module\.vault\[0\]\.init_gsa' terraform/backup.tf ||
  fail "production does not enforce a dedicated identity distinct from runtime/Vault"

workflow=".github/workflows/backup-verification.yaml"
rg -Fq 'service_account: ${{ secrets.BACKUP_RESTORE_GSA }}' "$workflow" ||
  fail "restore workflow does not authenticate as the dedicated identity"
if rg -Fq 'service_account: ${{ secrets.GSA }}' "$workflow"; then
  fail "restore workflow can fall back to the deployment identity"
fi
rg -q 'environment: production' "$workflow" || fail "restore identity is not protected by the production environment"

echo "Backup restore verification uses a dedicated least-privilege identity."
