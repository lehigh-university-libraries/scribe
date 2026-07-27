#!/usr/bin/env bash

set -euo pipefail
shopt -s inherit_errexit

readonly PURPOSE_LABEL="scribe-restore-drill"
readonly COS_IMAGE="projects/cos-cloud/global/images/cos-125-19216-395-4"
readonly MAX_SNAPSHOT_AGE_HOURS="${SCRIBE_RESTORE_MAX_SNAPSHOT_AGE_HOURS:-36}"
readonly MAX_SNAPSHOT_SKEW_SECONDS="${SCRIBE_RESTORE_MAX_SNAPSHOT_SKEW_SECONDS:-1800}"
readonly MAX_STALE_AGE_HOURS="${SCRIBE_RESTORE_MAX_STALE_AGE_HOURS:-6}"
readonly POLL_ATTEMPTS="${SCRIBE_RESTORE_POLL_ATTEMPTS:-120}"
readonly POLL_SECONDS="${SCRIBE_RESTORE_POLL_SECONDS:-10}"
readonly CREATE_ATTEMPTS="${SCRIBE_RESTORE_CREATE_ATTEMPTS:-12}"
readonly CREATE_RETRY_SECONDS="${SCRIBE_RESTORE_CREATE_RETRY_SECONDS:-60}"

fail() {
  echo "Cloud snapshot restore drill failed: $*" >&2
  return 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "$1 is required for the cloud snapshot restore drill" >&2
    exit 127
  }
}

require_positive_integer() {
  local name="$1" value="$2"
  [[ "$value" =~ ^[1-9][0-9]*$ ]] || {
    echo "$name must be a positive integer" >&2
    exit 2
  }
}

require_inputs() {
  : "${GCLOUD_PROJECT:?GCLOUD_PROJECT is required}"
  : "${PRODUCTION_INSTANCE:?PRODUCTION_INSTANCE is required}"
  : "${PRODUCTION_ZONE:?PRODUCTION_ZONE is required}"
  : "${PRODUCTION_REGION:?PRODUCTION_REGION is required}"
  : "${PRODUCTION_DATA_DISK:?PRODUCTION_DATA_DISK is required}"
  : "${PRODUCTION_DATA_DISK_SELF_LINK:?PRODUCTION_DATA_DISK_SELF_LINK is required}"
  : "${PRODUCTION_VOLUMES_DISK:?PRODUCTION_VOLUMES_DISK is required}"
  : "${PRODUCTION_VOLUMES_DISK_SELF_LINK:?PRODUCTION_VOLUMES_DISK_SELF_LINK is required}"
  : "${SCRIBE_DATA_GENERATION:?SCRIBE_DATA_GENERATION is required}"

  [[ "$GCLOUD_PROJECT" =~ ^[a-z][a-z0-9-]{4,28}[a-z0-9]$ ]] || fail "invalid GCP project" || exit 2
  [[ "$PRODUCTION_INSTANCE" =~ ^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$ ]] || fail "invalid production instance" || exit 2
  [[ "$PRODUCTION_ZONE" =~ ^[a-z]+-[a-z]+[0-9]+-[a-z]$ ]] || fail "invalid production zone" || exit 2
  [[ "$PRODUCTION_REGION" =~ ^[a-z]+-[a-z]+[0-9]+$ ]] || fail "invalid production region" || exit 2
  [[ "$PRODUCTION_ZONE" == "${PRODUCTION_REGION}-"* ]] || fail "production zone is outside the production region" || exit 2
  [[ "$PRODUCTION_DATA_DISK" =~ ^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$ ]] || fail "invalid production data disk" || exit 2
  [[ "$PRODUCTION_VOLUMES_DISK" =~ ^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$ ]] || fail "invalid production volumes disk" || exit 2
  [[ "$PRODUCTION_DATA_DISK" != "$PRODUCTION_VOLUMES_DISK" ]] || fail "production disks must be distinct" || exit 2
  [[ "$PRODUCTION_DATA_DISK_SELF_LINK" == */projects/"$GCLOUD_PROJECT"/zones/"$PRODUCTION_ZONE"/disks/"$PRODUCTION_DATA_DISK" ]] ||
    fail "production data-disk self link is not exact" || exit 2
  [[ "$PRODUCTION_VOLUMES_DISK_SELF_LINK" == */projects/"$GCLOUD_PROJECT"/zones/"$PRODUCTION_ZONE"/disks/"$PRODUCTION_VOLUMES_DISK" ]] ||
    fail "production volumes-disk self link is not exact" || exit 2
  [[ "$SCRIBE_DATA_GENERATION" =~ ^canonical-v(1|2)$ ]] || fail "data generation is not reviewed" || exit 2
}

run_id="${GITHUB_RUN_ID:-$(date -u +%s)}"
run_attempt="${GITHUB_RUN_ATTEMPT:-1}"
[[ "$run_id" =~ ^[0-9]{1,20}$ ]] || {
  echo "GITHUB_RUN_ID must be numeric" >&2
  exit 2
}
[[ "$run_attempt" =~ ^[0-9]{1,5}$ ]] || {
  echo "GITHUB_RUN_ATTEMPT must be numeric" >&2
  exit 2
}

resource_prefix="scribe-restore-${run_id}-${run_attempt}"
drill_instance="${resource_prefix}-vm"
restore_data_disk="${resource_prefix}-data"
restore_volumes_disk="${resource_prefix}-volumes"
# Retain the legacy name only so a labeled, snapshot-backed clone left by the
# former three-disk drill can be cleaned safely after this rollout.
legacy_restore_backup_disk="${resource_prefix}-backups"
readonly resource_prefix drill_instance restore_data_disk restore_volumes_disk legacy_restore_backup_disk run_id run_attempt

is_drill_name() {
  [[ "$1" =~ ^scribe-restore-[0-9]{1,20}-[0-9]{1,5}-(vm|data|volumes|backups)$ ]]
}

validate_labels() {
  local json="$1" expected_run_id="${2:-$run_id}" expected_run_attempt="${3:-$run_attempt}"
  jq -e \
    --arg purpose "$PURPOSE_LABEL" \
    --arg run_id "$expected_run_id" \
    --arg run_attempt "$expected_run_attempt" \
    '.labels.purpose == $purpose and .labels.run_id == $run_id and .labels.run_attempt == $run_attempt' \
    <<<"$json" >/dev/null
}

instance_json_if_present() {
  gcloud compute instances describe "$1" \
    --project "$GCLOUD_PROJECT" \
    --zone "$PRODUCTION_ZONE" \
    --format=json 2>/dev/null
}

disk_json_if_present() {
  gcloud compute disks describe "$1" \
    --project "$GCLOUD_PROJECT" \
    --zone "$PRODUCTION_ZONE" \
    --format=json 2>/dev/null
}

delete_drill_instance() {
  local name="$1" expected_run_id="${2:-$run_id}" expected_run_attempt="${3:-$run_attempt}" json
  is_drill_name "$name" || return 1
  json="$(instance_json_if_present "$name")" || return 0
  validate_labels "$json" "$expected_run_id" "$expected_run_attempt" || return 1
  gcloud compute instances delete "$name" \
    --project "$GCLOUD_PROJECT" \
    --zone "$PRODUCTION_ZONE" \
    --quiet
  ! instance_json_if_present "$name" >/dev/null
}

delete_drill_disk() {
  local name="$1" expected_run_id="${2:-$run_id}" expected_run_attempt="${3:-$run_attempt}" json
  is_drill_name "$name" || return 1
  [[ "$name" != "$PRODUCTION_DATA_DISK" && "$name" != "$PRODUCTION_VOLUMES_DISK" ]] || return 1
  json="$(disk_json_if_present "$name")" || return 0
  validate_labels "$json" "$expected_run_id" "$expected_run_attempt" || return 1
  jq -e '.sourceSnapshot | type == "string" and length > 0' <<<"$json" >/dev/null || return 1
  jq -e '(.users // []) | length == 0' <<<"$json" >/dev/null || return 1
  gcloud compute disks delete "$name" \
    --project "$GCLOUD_PROJECT" \
    --zone "$PRODUCTION_ZONE" \
    --quiet
  ! disk_json_if_present "$name" >/dev/null
}

cleanup_current() {
  local status=0
  delete_drill_instance "$drill_instance" || status=1
  delete_drill_disk "$restore_data_disk" || status=1
  delete_drill_disk "$restore_volumes_disk" || status=1
  delete_drill_disk "$legacy_restore_backup_disk" || status=1
  return "$status"
}

stale_resource() {
  local name="$1" created="$2" created_epoch now_epoch
  is_drill_name "$name" || return 1
  created_epoch="$(date -u -d "$created" +%s 2>/dev/null)" || return 1
  now_epoch="$(date -u +%s)"
  ((now_epoch - created_epoch > MAX_STALE_AGE_HOURS * 3600))
}

cleanup_stale() {
  local list name created listed_run listed_attempt status=0

  list="$(gcloud compute instances list \
    --project "$GCLOUD_PROJECT" \
    --filter="labels.purpose=${PURPOSE_LABEL}" \
    --format=json)"
  while IFS=$'\t' read -r name created listed_run listed_attempt; do
    [[ -n "$name" ]] || continue
    stale_resource "$name" "$created" || continue
    [[ "$listed_run" =~ ^[0-9]{1,20}$ && "$listed_attempt" =~ ^[0-9]{1,5}$ ]] || {
      status=1
      continue
    }
    delete_drill_instance "$name" "$listed_run" "$listed_attempt" || status=1
  done < <(jq -r '.[] | [.name, .creationTimestamp, .labels.run_id, .labels.run_attempt] | @tsv' <<<"$list")

  list="$(gcloud compute disks list \
    --project "$GCLOUD_PROJECT" \
    --filter="zone:(${PRODUCTION_ZONE}) AND labels.purpose=${PURPOSE_LABEL}" \
    --format=json)"
  while IFS=$'\t' read -r name created listed_run listed_attempt; do
    [[ -n "$name" ]] || continue
    stale_resource "$name" "$created" || continue
    [[ "$listed_run" =~ ^[0-9]{1,20}$ && "$listed_attempt" =~ ^[0-9]{1,5}$ ]] || {
      status=1
      continue
    }
    delete_drill_disk "$name" "$listed_run" "$listed_attempt" || status=1
  done < <(jq -r '.[] | [.name, .creationTimestamp, .labels.run_id, .labels.run_attempt] | @tsv' <<<"$list")

  return "$status"
}

latest_daily_snapshot() {
  local source_self_link="$1" source_resource snapshots
  source_resource="${source_self_link#*'/compute/v1/'}"
  [[ "$source_resource" == projects/"$GCLOUD_PROJECT"/zones/"$PRODUCTION_ZONE"/disks/* ]] ||
    fail "snapshot source identity is invalid"
  snapshots="$(gcloud compute snapshots list \
    --project "$GCLOUD_PROJECT" \
    --filter="status=READY AND labels.managed_by=terraform AND labels.instance=${PRODUCTION_INSTANCE}" \
    --format=json)"
  jq -cer \
    --arg source "$source_resource" \
    --arg instance "$PRODUCTION_INSTANCE" '
      [.[] | select(
        .status == "READY" and
        .autoCreated == true and
        (.sourceDisk | endswith($source)) and
        .labels.managed_by == "terraform" and
        .labels.instance == $instance
      )] | sort_by(.creationTimestamp) | last // empty
    ' <<<"$snapshots"
}

snapshot_epoch() {
  local json="$1" created epoch now
  created="$(jq -er '.creationTimestamp' <<<"$json")"
  epoch="$(date -u -d "$created" +%s)" || fail "snapshot has an invalid creation timestamp"
  now="$(date -u +%s)"
  ((epoch <= now + 300)) || fail "snapshot timestamp is in the future"
  ((now - epoch <= MAX_SNAPSHOT_AGE_HOURS * 3600)) || fail "snapshot is older than ${MAX_SNAPSHOT_AGE_HOURS} hours"
  printf '%s\n' "$epoch"
}

validate_source_disk() {
  local disk="$1" expected_self_link="$2" expected_resource json expected_daily expected_weekly
  json="$(gcloud compute disks describe "$disk" \
    --project "$GCLOUD_PROJECT" \
    --zone "$PRODUCTION_ZONE" \
    --format=json)"
  expected_resource="${expected_self_link#*'/compute/v1/'}"
  jq -e --arg resource "$expected_resource" '(.selfLink | endswith($resource)) and .status == "READY"' <<<"$json" >/dev/null ||
    fail "source disk ${disk} is not the expected READY production disk"
  expected_daily="/regions/${PRODUCTION_REGION}/resourcePolicies/${PRODUCTION_INSTANCE}-daily-snapshot"
  expected_weekly="/regions/${PRODUCTION_REGION}/resourcePolicies/${PRODUCTION_INSTANCE}-weekly-snapshot"
  jq -e --arg suffix "$expected_daily" 'any(.resourcePolicies[]?; endswith($suffix))' <<<"$json" >/dev/null ||
    fail "source disk ${disk} is missing its daily snapshot policy"
  jq -e --arg suffix "$expected_weekly" 'any(.resourcePolicies[]?; endswith($suffix))' <<<"$json" >/dev/null ||
    fail "source disk ${disk} is missing its weekly snapshot policy"
  printf '%s\n' "$json"
}

validate_restore_firewall() {
  local network="$1" family rule json expected_range
  for family in ipv4 ipv6; do
    rule="${PRODUCTION_INSTANCE}-restore-drill-deny-egress-${family}"
    expected_range="0.0.0.0/0"
    [[ "$family" == "ipv6" ]] && expected_range="::/0"
    json="$(gcloud compute firewall-rules describe "$rule" \
      --project "$GCLOUD_PROJECT" \
      --format=json)"
    jq -e \
      --arg network "$network" \
      --arg range "$expected_range" '
        .direction == "EGRESS" and
        .priority == 0 and
        (.disabled // false) == false and
        .network == $network and
        .destinationRanges == [$range] and
        .targetTags == ["scribe-restore-drill"] and
        any(.denied[]?; .IPProtocol == "all")
      ' <<<"$json" >/dev/null || fail "restore-drill ${family} deny-egress firewall is not exact"
  done
}

create_restore_disk() {
  local name="$1" snapshot_json="$2" source_json="$3" snapshot size restored output attempt created=false
  snapshot="$(jq -er '.name' <<<"$snapshot_json")"
  [[ "$snapshot" =~ ^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$ ]] || fail "snapshot name is invalid"
  size="$(jq -er '.sizeGb' <<<"$source_json")"
  [[ "$size" =~ ^[1-9][0-9]*$ ]] || fail "source disk size is invalid"
  jq -e --arg size "$size" '.diskSizeGb == $size' <<<"$snapshot_json" >/dev/null ||
    fail "snapshot size does not match its source disk"

  # Restore into a disposable Persistent Disk. The production Hyperdisk type
  # does not have a portable read-only attachment mode, while snapshots permit
  # choosing a different destination disk type.
  for attempt in $(seq 1 "$CREATE_ATTEMPTS"); do
    if output="$(gcloud compute disks create "$name" \
      --project "$GCLOUD_PROJECT" \
      --zone "$PRODUCTION_ZONE" \
      --source-snapshot "$snapshot" \
      --type=pd-balanced \
      --size="$size" \
      --labels="purpose=${PURPOSE_LABEL},run_id=${run_id},run_attempt=${run_attempt}" \
      --quiet 2>&1)"; then
      created=true
      break
    fi
    if ! grep -Eqi 'RESOURCE_OPERATION_RATE_EXCEEDED|once every 10 minutes|rate.*exceeded' <<<"$output" ||
      ((attempt == CREATE_ATTEMPTS)); then
      printf '%s\n' "$output" >&2
      fail "could not materialize snapshot ${snapshot}"
    fi
    sleep "$CREATE_RETRY_SECONDS"
  done
  [[ "$created" == "true" ]] || fail "could not materialize snapshot ${snapshot}"

  restored="$(disk_json_if_present "$name")" || fail "restored disk ${name} cannot be described"
  validate_labels "$restored" || fail "restored disk ${name} labels changed"
  jq -e \
    --arg snapshot "/global/snapshots/${snapshot}" \
    --arg size "$size" '
      .status == "READY" and
      .sizeGb == $size and
      (.sourceSnapshot | endswith($snapshot)) and
      ((.users // []) | length == 0)
    ' <<<"$restored" >/dev/null || fail "restored disk ${name} failed provenance/readiness checks"
}

write_startup_probe() {
  local destination="$1"
  cat >"$destination" <<'PROBE'
#!/bin/bash
set -Eeuo pipefail

exec > >(tee -a /dev/ttyS0) 2>&1

finish() {
  local status="$1"
  if [[ "$status" -eq 0 ]]; then
    echo "SCRIBE_RESTORE_PROBE_OK"
  else
    echo "SCRIBE_RESTORE_PROBE_FAILED"
  fi
  sync
  sleep 2
  shutdown -h now >/dev/null 2>&1 || true
  exit "$status"
}
trap 'finish 1' ERR

wait_for_device() {
  local device="$1"
  for _ in $(seq 1 90); do
    [[ -b "$device" ]] && return 0
    sleep 2
  done
  return 1
}

mount_clone() {
  local device="$1" target="$2" filesystem
  wait_for_device "$device"
  filesystem="$(blkid -p -s TYPE -o value -- "$device")"
  [[ "$filesystem" == "ext4" ]]
  mkdir -p -- "$target"
  mount -t ext4 -o ro,noload -- "$device" "$target"
  mountpoint -q -- "$target"
}

mount_clone /dev/disk/by-id/google-restore-data /mnt/restore-data
mount_clone /dev/disk/by-id/google-restore-volumes /mnt/restore-volumes

backup="$(find /mnt/restore-data/backups/mariadb/primary -maxdepth 1 -type f -name '*-primary.sql.gz' -printf '%T@\t%p\n' | sort -nr | awk -F '\t' 'NR == 1 {print $2}')"
[[ -n "$backup" && -f "$backup" && ! -L "$backup" && -s "$backup" ]]
backup_epoch="$(stat -c %Y -- "$backup")"
now_epoch="$(date -u +%s)"
((now_epoch >= backup_epoch && now_epoch - backup_epoch <= 172800))
gzip -t -- "$backup"
marker="/mnt/restore-data/backups/mariadb/.last-success"
[[ -f "$marker" && ! -L "$marker" ]]
marker_date="$(tr -d '\n' <"$marker")"
[[ "$marker_date" =~ ^[0-9]{8}$ && "$(basename -- "$backup")" == "${marker_date}-primary.sql.gz" ]]
gzip -cd -- "$backup" | awk '
  /`scribe_schema_migrations`/ { migrations = 1 }
  /`annotation_pages`/ { pages = 1 }
  /`transcription_jobs`/ { jobs = 1 }
  /`annotation_mirror_outbox`/ { outbox = 1 }
  END { exit !(migrations && pages && jobs && outbox) }
'

mariadb_volume="$(find "/mnt/restore-volumes" -mindepth 1 -maxdepth 1 -type d -name "scribe-prod-__SCRIBE_DATA_GENERATION__-mariadb-data" -print -quit)"
[[ -n "$mariadb_volume" && -d "$mariadb_volume/_data" && ! -L "$mariadb_volume/_data" ]]
[[ -n "$(find "$mariadb_volume/_data" -mindepth 1 -maxdepth 1 -print -quit)" ]]

trap - ERR
finish 0
PROBE
  sed -i "s/__SCRIBE_DATA_GENERATION__/${SCRIBE_DATA_GENERATION}/g" "$destination"
  chmod 0700 "$destination"
}

run_drill() {
  local data_source_json volumes_source_json data_snapshot volumes_snapshot
  local data_epoch volumes_epoch oldest_epoch newest_epoch skew source_instance_json network subnetwork
  local temp_dir startup_script serial status

  cleanup_stale || fail "stale restore resources could not be cleaned safely"
  cleanup_current || fail "resources from this run attempt could not be cleaned safely"

  data_source_json="$(validate_source_disk "$PRODUCTION_DATA_DISK" "$PRODUCTION_DATA_DISK_SELF_LINK")"
  volumes_source_json="$(validate_source_disk "$PRODUCTION_VOLUMES_DISK" "$PRODUCTION_VOLUMES_DISK_SELF_LINK")"
  data_snapshot="$(latest_daily_snapshot "$PRODUCTION_DATA_DISK_SELF_LINK")" || fail "no exact READY daily data-disk snapshot exists"
  volumes_snapshot="$(latest_daily_snapshot "$PRODUCTION_VOLUMES_DISK_SELF_LINK")" || fail "no exact READY daily volumes-disk snapshot exists"
  data_epoch="$(snapshot_epoch "$data_snapshot")"
  volumes_epoch="$(snapshot_epoch "$volumes_snapshot")"
  oldest_epoch="$data_epoch"
  newest_epoch="$data_epoch"
  ((volumes_epoch < oldest_epoch)) && oldest_epoch="$volumes_epoch"
  ((volumes_epoch > newest_epoch)) && newest_epoch="$volumes_epoch"
  skew=$((newest_epoch - oldest_epoch))
  ((skew <= MAX_SNAPSHOT_SKEW_SECONDS)) || fail "latest production snapshots differ by more than ${MAX_SNAPSHOT_SKEW_SECONDS} seconds"

  create_restore_disk "$restore_data_disk" "$data_snapshot" "$data_source_json"
  create_restore_disk "$restore_volumes_disk" "$volumes_snapshot" "$volumes_source_json"

  source_instance_json="$(gcloud compute instances describe "$PRODUCTION_INSTANCE" \
    --project "$GCLOUD_PROJECT" \
    --zone "$PRODUCTION_ZONE" \
    --format=json)"
  network="$(jq -er '.networkInterfaces[0].network' <<<"$source_instance_json")"
  subnetwork="$(jq -er '.networkInterfaces[0].subnetwork' <<<"$source_instance_json")"
  [[ "$network" == */projects/*/global/networks/* ]] || fail "production network is invalid"
  [[ "$subnetwork" == */projects/*/regions/"$PRODUCTION_REGION"/subnetworks/* ]] || fail "production subnetwork is invalid"
  validate_restore_firewall "$network"

  temp_dir="$(mktemp -d "${RUNNER_TEMP:-/tmp}/scribe-restore-drill.XXXXXX")"
  startup_script="${temp_dir}/startup.sh"
  write_startup_probe "$startup_script"

  gcloud compute instances create "$drill_instance" \
    --project "$GCLOUD_PROJECT" \
    --zone "$PRODUCTION_ZONE" \
    --machine-type=e2-small \
    --image "$COS_IMAGE" \
    --network-interface="network=${network},subnet=${subnetwork},no-address" \
    --tags=scribe-restore-drill \
    --no-service-account \
    --no-scopes \
    --no-restart-on-failure \
    --maintenance-policy=TERMINATE \
    --shielded-secure-boot \
    --shielded-vtpm \
    --shielded-integrity-monitoring \
    --disk="name=${restore_data_disk},device-name=restore-data,mode=ro,boot=no,auto-delete=no" \
    --disk="name=${restore_volumes_disk},device-name=restore-volumes,mode=ro,boot=no,auto-delete=no" \
    --metadata=serial-port-enable=true \
    --metadata-from-file="startup-script=${startup_script}" \
    --labels="purpose=${PURPOSE_LABEL},run_id=${run_id},run_attempt=${run_attempt}" \
    --quiet
  rm -rf -- "$temp_dir"

  for _ in $(seq 1 "$POLL_ATTEMPTS"); do
    serial="$(gcloud compute instances get-serial-port-output "$drill_instance" \
      --project "$GCLOUD_PROJECT" \
      --zone "$PRODUCTION_ZONE" \
      --port=1 2>&1 || true)"
    if grep -q '^SCRIBE_RESTORE_PROBE_OK$' <<<"$serial"; then
      echo "Cloud snapshot restore drill passed for snapshots $(jq -r '.name' <<<"$data_snapshot") and $(jq -r '.name' <<<"$volumes_snapshot")."
      return 0
    fi
    if grep -q '^SCRIBE_RESTORE_PROBE_FAILED$' <<<"$serial"; then
      tail -n 200 <<<"$serial" >&2
      fail "the isolated read-only VM rejected the restored backup"
    fi
    status="$(instance_json_if_present "$drill_instance" | jq -r '.status // empty')"
    if [[ "$status" == "TERMINATED" ]]; then
      tail -n 200 <<<"$serial" >&2
      fail "the isolated restore VM terminated without a success marker"
    fi
    sleep "$POLL_SECONDS"
  done

  tail -n 200 <<<"${serial:-}" >&2
  fail "timed out waiting for the isolated restore VM"
}

require_command gcloud
require_command jq
require_command date
require_inputs
require_positive_integer SCRIBE_RESTORE_MAX_SNAPSHOT_AGE_HOURS "$MAX_SNAPSHOT_AGE_HOURS"
require_positive_integer SCRIBE_RESTORE_MAX_SNAPSHOT_SKEW_SECONDS "$MAX_SNAPSHOT_SKEW_SECONDS"
require_positive_integer SCRIBE_RESTORE_MAX_STALE_AGE_HOURS "$MAX_STALE_AGE_HOURS"
require_positive_integer SCRIBE_RESTORE_POLL_ATTEMPTS "$POLL_ATTEMPTS"
require_positive_integer SCRIBE_RESTORE_POLL_SECONDS "$POLL_SECONDS"
require_positive_integer SCRIBE_RESTORE_CREATE_ATTEMPTS "$CREATE_ATTEMPTS"
require_positive_integer SCRIBE_RESTORE_CREATE_RETRY_SECONDS "$CREATE_RETRY_SECONDS"

case "${1:-run}" in
  run)
    trap 'status=$?; trap - EXIT INT TERM; cleanup_current || status=1; exit "$status"' EXIT INT TERM
    run_drill
    ;;
  --cleanup-current)
    cleanup_current
    ;;
  --cleanup-stale)
    cleanup_stale
    ;;
  *)
    echo "Usage: $0 [run|--cleanup-current|--cleanup-stale]" >&2
    exit 2
    ;;
esac
