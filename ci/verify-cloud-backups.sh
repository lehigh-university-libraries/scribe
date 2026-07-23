#!/usr/bin/env bash

set -euo pipefail

scope="${BACKUP_AUDIT_SCOPE:-full}"
minimum_retention_seconds="${BACKUP_MINIMUM_RETENTION_SECONDS:-1209600}"
maximum_transfer_age_hours="${BACKUP_MAXIMUM_TRANSFER_AGE_HOURS:-36}"
fixture_dir="${BACKUP_AUDIT_FIXTURE_DIR:-}"

case "$scope" in
  state|full) ;;
  *) echo "BACKUP_AUDIT_SCOPE must be state or full" >&2; exit 2 ;;
esac

if [[ ! "$minimum_retention_seconds" =~ ^[0-9]+$ ]] || [ "$minimum_retention_seconds" -lt 604800 ]; then
  echo "BACKUP_MINIMUM_RETENTION_SECONDS must be an integer of at least 604800" >&2
  exit 2
fi
if [[ ! "$maximum_transfer_age_hours" =~ ^[0-9]+$ ]] || [ "$maximum_transfer_age_hours" -lt 1 ]; then
  echo "BACKUP_MAXIMUM_TRANSFER_AGE_HOURS must be a positive integer" >&2
  exit 2
fi

bucket_json() {
  local bucket="$1"
  if [ -n "$fixture_dir" ]; then
    cat "$fixture_dir/${bucket}.json"
    return
  fi
  # Keep this aligned with the Storage JSON API shape used by the bootstrap.
  # Recent Cloud SDK normalization omits or renames retention fields.
  gcloud storage buckets describe "gs://${bucket}" --raw --format=json
}

verify_bucket() {
  local label="$1"
  local bucket="$2"
  local json
  if [ -z "$bucket" ]; then
    echo "$label bucket name is empty" >&2
    return 1
  fi
  json="$(bucket_json "$bucket")"
  if ! jq -e --argjson minimum "$minimum_retention_seconds" '
    def seconds:
      if . == null then 0
      elif type == "number" then .
      elif type == "string" then (capture("(?<n>[0-9]+)").n | tonumber)
      else 0 end;
    (.versioning.enabled == true) and
    ([
      (.softDeletePolicy.retentionDurationSeconds | seconds),
      (.retentionPolicy.retentionPeriod | seconds)
    ] | max >= $minimum)
  ' <<<"$json" >/dev/null; then
    echo "$label bucket gs://${bucket} must have versioning and at least ${minimum_retention_seconds}s retention or soft delete" >&2
    return 1
  fi
  echo "Verified $label bucket gs://${bucket}."
}

: "${TF_STATE_BUCKET:?TF_STATE_BUCKET is required}"
verify_bucket "Terraform state" "$TF_STATE_BUCKET"

if [ "$scope" = "state" ]; then
  exit 0
fi

: "${UPLOADS_BUCKET:?UPLOADS_BUCKET is required}"
: "${UPLOADS_BACKUP_BUCKET:?UPLOADS_BACKUP_BUCKET is required}"
: "${UPLOADS_BACKUP_TRANSFER_JOB:?UPLOADS_BACKUP_TRANSFER_JOB is required}"
: "${VAULT_DATA_BUCKET:?VAULT_DATA_BUCKET is required}"
: "${VAULT_KEY_BUCKET:?VAULT_KEY_BUCKET is required}"

verify_bucket "source uploads" "$UPLOADS_BUCKET"
verify_bucket "uploads backup" "$UPLOADS_BACKUP_BUCKET"
verify_bucket "Vault data" "$VAULT_DATA_BUCKET"
verify_bucket "Vault key material" "$VAULT_KEY_BUCKET"

if [ -n "$fixture_dir" ]; then
  operations="$(cat "$fixture_dir/transfer-operations.json")"
else
  operations="$(gcloud transfer operations list --job-names="$UPLOADS_BACKUP_TRANSFER_JOB" --format=json)"
fi

last_success="$(jq -r '
  [.[] | select((.metadata.status // .status // "") == "SUCCESS") |
    (.metadata.endTime // .endTime // "")] |
  map(select(length > 0)) | sort | last // ""
' <<<"$operations")"

if [ -z "$last_success" ]; then
  if [ "${ALLOW_MISSING_TRANSFER_OPERATION:-false}" = "true" ]; then
    echo "No successful transfer exists yet; policy verification passed for a newly created job."
    exit 0
  fi
  echo "No successful operation found for transfer job ${UPLOADS_BACKUP_TRANSFER_JOB}" >&2
  exit 1
fi

last_epoch="$(date -u -d "$last_success" +%s)"
now_epoch="$(date -u +%s)"
maximum_age_seconds="$((maximum_transfer_age_hours * 3600))"
if [ "$((now_epoch - last_epoch))" -gt "$maximum_age_seconds" ]; then
  echo "Last successful upload backup (${last_success}) is older than ${maximum_transfer_age_hours} hours" >&2
  exit 1
fi

echo "Verified upload backup freshness at ${last_success}."
