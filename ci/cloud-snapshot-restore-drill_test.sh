#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/scribe-cloud-restore-test.XXXXXX")"
trap 'rm -rf -- "$TEMP_DIR"' EXIT
mkdir -p "$TEMP_DIR/bin" "$TEMP_DIR/state/disks" "$TEMP_DIR/state/instances"

cat >"$TEMP_DIR/bin/gcloud" <<'FAKE_GCLOUD'
#!/usr/bin/env bash
set -euo pipefail

echo "$*" >>"$MOCK_GCLOUD_LOG"
group="${1:-}"
kind="${2:-}"
action="${3:-}"
name="${4:-}"
now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
[[ "${MOCK_OLD_SNAPSHOTS:-false}" == "true" ]] && now="2000-01-01T00:00:00Z"
project="scribe-test"
zone="us-east5-b"
region="us-east5"

if [[ "$group $kind $action" == "compute snapshots list" ]]; then
  data_source="https://compute.googleapis.com/compute/v1/projects/${project}/zones/${zone}/disks/scribe-data-disk"
  [[ "${MOCK_WRONG_SOURCE:-false}" == "true" ]] && data_source="https://compute.googleapis.com/compute/v1/projects/${project}/zones/${zone}/disks/not-production"
  cat <<JSON
[
  {
    "name": "scribe-data-daily-1",
    "status": "READY",
    "autoCreated": true,
    "sourceDisk": "${data_source}",
    "creationTimestamp": "${now}",
    "diskSizeGb": "20",
    "labels": {"managed_by": "terraform", "instance": "scribe"}
  },
  {
    "name": "scribe-volumes-daily-1",
    "status": "READY",
    "autoCreated": true,
    "sourceDisk": "https://compute.googleapis.com/compute/v1/projects/${project}/zones/${zone}/disks/scribe-docker-volumes",
    "creationTimestamp": "${now}",
    "diskSizeGb": "50",
    "labels": {"managed_by": "terraform", "instance": "scribe"}
  }
]
JSON
  exit 0
fi

if [[ "$group $kind $action" == "compute disks list" || "$group $kind $action" == "compute instances list" ]]; then
  echo '[]'
  exit 0
fi

if [[ "$group $kind $action" == "compute firewall-rules describe" ]]; then
  range="0.0.0.0/0"
  [[ "$name" == *-ipv6 ]] && range="::/0"
  cat <<JSON
{"name":"${name}","direction":"EGRESS","priority":0,"disabled":false,"network":"https://www.googleapis.com/compute/v1/projects/${project}/global/networks/scribe","destinationRanges":["${range}"],"targetTags":["scribe-restore-drill"],"denied":[{"IPProtocol":"all"}]}
JSON
  exit 0
fi

if [[ "$group $kind $action" == "compute disks describe" ]]; then
  case "$name" in
    scribe-data-disk)
      size=20
      ;;
    scribe-docker-volumes)
      size=50
      ;;
    *)
      state="$MOCK_GCLOUD_STATE/disks/${name}"
      [[ -f "$state" ]] || exit 1
      # shellcheck disable=SC1090
      source "$state"
      cat <<JSON
{"name":"${name}","status":"READY","sizeGb":"${size}","sourceSnapshot":"https://www.googleapis.com/compute/v1/projects/${project}/global/snapshots/${snapshot}","users":[],"creationTimestamp":"${now}","labels":{"purpose":"scribe-restore-drill","run_id":"12345","run_attempt":"2"}}
JSON
      exit 0
      ;;
  esac
  cat <<JSON
{"name":"${name}","status":"READY","sizeGb":"${size}","selfLink":"https://compute.googleapis.com/compute/v1/projects/${project}/zones/${zone}/disks/${name}","type":"https://www.googleapis.com/compute/v1/projects/${project}/zones/${zone}/diskTypes/hyperdisk-balanced","resourcePolicies":["https://www.googleapis.com/compute/v1/projects/${project}/regions/${region}/resourcePolicies/scribe-daily-snapshot","https://www.googleapis.com/compute/v1/projects/${project}/regions/${region}/resourcePolicies/scribe-weekly-snapshot"]}
JSON
  exit 0
fi

if [[ "$group $kind $action" == "compute disks create" ]]; then
  if [[ "${MOCK_FAIL_VOLUMES_CREATE:-false}" == "true" && "$name" == *-volumes ]]; then
    echo "injected volumes create failure" >&2
    exit 1
  fi
  snapshot=""
  size=""
  previous=""
  for argument in "$@"; do
    if [[ "$previous" == "--source-snapshot" ]]; then snapshot="$argument"; fi
    case "$argument" in
      --size=*) size="${argument#--size=}" ;;
    esac
    previous="$argument"
  done
  [[ -n "$snapshot" && "$size" =~ ^[0-9]+$ ]]
  printf 'snapshot=%q\nsize=%q\n' "$snapshot" "$size" >"$MOCK_GCLOUD_STATE/disks/${name}"
  exit 0
fi

if [[ "$group $kind $action" == "compute disks delete" ]]; then
  if [[ "${MOCK_CLEANUP_FAIL:-false}" == "true" && "$name" == *-volumes ]]; then
    exit 1
  fi
  rm -f -- "$MOCK_GCLOUD_STATE/disks/${name}"
  exit 0
fi

if [[ "$group $kind $action" == "compute instances describe" ]]; then
  if [[ "$name" == "scribe" ]]; then
    cat <<JSON
{"name":"scribe","status":"RUNNING","networkInterfaces":[{"network":"https://www.googleapis.com/compute/v1/projects/${project}/global/networks/scribe","subnetwork":"https://www.googleapis.com/compute/v1/projects/${project}/regions/${region}/subnetworks/scribe"}]}
JSON
    exit 0
  fi
  [[ -f "$MOCK_GCLOUD_STATE/instances/${name}" ]] || exit 1
  cat <<JSON
{"name":"${name}","status":"RUNNING","creationTimestamp":"${now}","labels":{"purpose":"scribe-restore-drill","run_id":"12345","run_attempt":"2"}}
JSON
  exit 0
fi

if [[ "$group $kind $action" == "compute instances create" ]]; then
  startup_script=""
  for argument in "$@"; do
    case "$argument" in
      --metadata-from-file=startup-script=*) startup_script="${argument#--metadata-from-file=startup-script=}" ;;
    esac
  done
  [[ -f "$startup_script" ]]
  grep -Fq '/mnt/disks/restore-data/backups/mariadb/primary' "$startup_script"
  grep -Fq '/mnt/disks/restore-data/backups/mariadb/.last-success' "$startup_script"
  grep -Fq 'mount_clone /dev/disk/by-id/google-restore-volumes /mnt/disks/restore-volumes' "$startup_script"
  grep -Fq 'sleep 1800' "$startup_script"
  if grep -Fq '/mnt/restore-backups' "$startup_script"; then
    echo "startup probe still expects a third restore disk" >&2
    exit 1
  fi
  touch "$MOCK_GCLOUD_STATE/instances/${name}"
  exit 0
fi

if [[ "$group $kind $action" == "compute instances get-serial-port-output" ]]; then
  if [[ "${MOCK_MISSING_MARKER:-false}" == "true" ]]; then
    echo "probe is still booting"
  else
    printf 'SCRIBE_RESTORE_PROBE_OK\r\n'
  fi
  exit 0
fi

if [[ "$group $kind $action" == "compute instances delete" ]]; then
  rm -f -- "$MOCK_GCLOUD_STATE/instances/${name}"
  exit 0
fi

echo "unexpected fake gcloud invocation: $*" >&2
exit 99
FAKE_GCLOUD
chmod +x "$TEMP_DIR/bin/gcloud"

export PATH="$TEMP_DIR/bin:$PATH"
export MOCK_GCLOUD_STATE="$TEMP_DIR/state"
export MOCK_GCLOUD_LOG="$TEMP_DIR/gcloud.log"
export GCLOUD_PROJECT=scribe-test
export PRODUCTION_INSTANCE=scribe
export PRODUCTION_ZONE=us-east5-b
export PRODUCTION_REGION=us-east5
export PRODUCTION_DATA_DISK=scribe-data-disk
export PRODUCTION_DATA_DISK_SELF_LINK=https://www.googleapis.com/compute/v1/projects/scribe-test/zones/us-east5-b/disks/scribe-data-disk
export PRODUCTION_VOLUMES_DISK=scribe-docker-volumes
export PRODUCTION_VOLUMES_DISK_SELF_LINK=https://www.googleapis.com/compute/v1/projects/scribe-test/zones/us-east5-b/disks/scribe-docker-volumes
export SCRIBE_DATA_GENERATION=canonical-v1
export GITHUB_RUN_ID=12345
export GITHUB_RUN_ATTEMPT=2
export SCRIBE_RESTORE_POLL_ATTEMPTS=1
export SCRIBE_RESTORE_POLL_SECONDS=1
export SCRIBE_RESTORE_CREATE_ATTEMPTS=1
export SCRIBE_RESTORE_CREATE_RETRY_SECONDS=1

reset_fixture() {
  rm -f -- "$MOCK_GCLOUD_LOG" "$MOCK_GCLOUD_STATE"/disks/* "$MOCK_GCLOUD_STATE"/instances/*
  unset MOCK_OLD_SNAPSHOTS MOCK_WRONG_SOURCE MOCK_FAIL_VOLUMES_CREATE MOCK_MISSING_MARKER MOCK_CLEANUP_FAIL
}

assert_no_fixture_resources() {
  ! find "$MOCK_GCLOUD_STATE/disks" "$MOCK_GCLOUD_STATE/instances" -type f -print -quit | grep -q .
}

reset_fixture
"$ROOT_DIR/ci/cloud-snapshot-restore-drill.sh"
assert_no_fixture_resources
grep -q -- '--type=pd-balanced' "$MOCK_GCLOUD_LOG"
grep -q -- '--machine-type=e2-small' "$MOCK_GCLOUD_LOG"
grep -q -- '--maintenance-policy=MIGRATE' "$MOCK_GCLOUD_LOG"
grep -q -- '--no-service-account' "$MOCK_GCLOUD_LOG"
grep -q -- '--tags=scribe-restore-drill' "$MOCK_GCLOUD_LOG"
grep -q -- 'device-name=restore-data,mode=ro,boot=no,auto-delete=no' "$MOCK_GCLOUD_LOG"
grep -q -- 'device-name=restore-volumes,mode=ro,boot=no,auto-delete=no' "$MOCK_GCLOUD_LOG"
if grep -q -- 'device-name=restore-backups' "$MOCK_GCLOUD_LOG"; then
  echo "restore drill unexpectedly attached a third disk" >&2
  exit 1
fi
if grep -Eq 'compute (instances|disks) delete (scribe|scribe-data-disk|scribe-docker-volumes|scribe-mariadb-backups)( |$)' "$MOCK_GCLOUD_LOG"; then
  echo "restore drill attempted to delete a production resource" >&2
  exit 1
fi

reset_fixture
export MOCK_WRONG_SOURCE=true
if "$ROOT_DIR/ci/cloud-snapshot-restore-drill.sh" >/dev/null 2>&1; then
  echo "wrong-source snapshot fixture unexpectedly passed" >&2
  exit 1
fi
if grep -q 'compute disks create' "$MOCK_GCLOUD_LOG"; then
  echo "wrong-source snapshot fixture created a disk" >&2
  exit 1
fi
assert_no_fixture_resources

reset_fixture
export MOCK_OLD_SNAPSHOTS=true
if "$ROOT_DIR/ci/cloud-snapshot-restore-drill.sh" >/dev/null 2>&1; then
  echo "stale snapshot fixture unexpectedly passed" >&2
  exit 1
fi
if grep -q 'compute disks create' "$MOCK_GCLOUD_LOG"; then
  echo "stale snapshot fixture created a disk" >&2
  exit 1
fi
assert_no_fixture_resources

reset_fixture
export MOCK_FAIL_VOLUMES_CREATE=true
if "$ROOT_DIR/ci/cloud-snapshot-restore-drill.sh" >/dev/null 2>&1; then
  echo "partial restore fixture unexpectedly passed" >&2
  exit 1
fi
assert_no_fixture_resources

reset_fixture
export MOCK_MISSING_MARKER=true
if "$ROOT_DIR/ci/cloud-snapshot-restore-drill.sh" >/dev/null 2>&1; then
  echo "missing marker fixture unexpectedly passed" >&2
  exit 1
fi
assert_no_fixture_resources

reset_fixture
export MOCK_CLEANUP_FAIL=true
if "$ROOT_DIR/ci/cloud-snapshot-restore-drill.sh" >/dev/null 2>&1; then
  echo "cleanup failure fixture unexpectedly passed" >&2
  exit 1
fi
find "$MOCK_GCLOUD_STATE/disks" -type f -name '*-volumes' -print -quit | grep -q .

echo "Cloud snapshot restore drill fixtures passed."
