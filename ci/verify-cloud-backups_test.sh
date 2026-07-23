#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fixture_dir="$(mktemp -d)"
trap 'rm -rf "$fixture_dir"' EXIT

good_bucket='{"versioning":{"enabled":true},"softDeletePolicy":{"retentionDurationSeconds":"2592000"}}'
for bucket in state uploads uploads-backup vault-data vault-key; do
  printf '%s\n' "$good_bucket" > "$fixture_dir/${bucket}.json"
done
printf '[{"metadata":{"status":"SUCCESS","endTime":"%s"}}]\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$fixture_dir/transfer-operations.json"

BACKUP_AUDIT_FIXTURE_DIR="$fixture_dir" \
TF_STATE_BUCKET=state \
UPLOADS_BUCKET=uploads \
UPLOADS_BACKUP_BUCKET=uploads-backup \
UPLOADS_BACKUP_TRANSFER_JOB=transferJobs/test \
VAULT_DATA_BUCKET=vault-data \
VAULT_KEY_BUCKET=vault-key \
  "$ROOT_DIR/ci/verify-cloud-backups.sh" >/dev/null

printf '%s\n' '{"versioning":{"enabled":false},"softDeletePolicy":{"retentionDurationSeconds":"2592000"}}' > "$fixture_dir/state.json"
if BACKUP_AUDIT_FIXTURE_DIR="$fixture_dir" TF_STATE_BUCKET=state BACKUP_AUDIT_SCOPE=state \
  "$ROOT_DIR/ci/verify-cloud-backups.sh" >/dev/null 2>&1; then
  echo "backup verifier accepted a state bucket without versioning" >&2
  exit 1
fi

mkdir -p "$fixture_dir/bin"
cat >"$fixture_dir/bin/gcloud" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >"$FAKE_GCLOUD_LOG"
printf '%s\n' "$FAKE_BUCKET_JSON"
EOF
chmod +x "$fixture_dir/bin/gcloud"
PATH="$fixture_dir/bin:$PATH" \
FAKE_GCLOUD_LOG="$fixture_dir/gcloud.log" \
FAKE_BUCKET_JSON="$good_bucket" \
TF_STATE_BUCKET=state \
BACKUP_AUDIT_SCOPE=state \
  "$ROOT_DIR/ci/verify-cloud-backups.sh" >/dev/null
grep -Fxq 'storage buckets describe gs://state --raw --format=json' "$fixture_dir/gcloud.log" || {
  echo "backup verifier must inspect the raw Storage JSON API resource" >&2
  exit 1
}

echo "Cloud backup verification contracts passed."
