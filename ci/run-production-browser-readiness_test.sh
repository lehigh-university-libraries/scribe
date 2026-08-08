#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d "${TMPDIR:-/tmp}/scribe-production-browser-readiness-test.XXXXXX")"
readonly ROOT_DIR TEST_DIR
readonly TEST_RUN_ID=76543210
readonly TEST_RUN_ATTEMPT=3
readonly TEST_JOB=scribe-browser-acde1234
readonly TEST_SECRET=scribe-browser-session-acde1234
readonly TEST_PROJECT=scribe-test
readonly TEST_PROJECT_NUMBER=123456789012
readonly TEST_REGION=us-east5
readonly TEST_ZONE=us-east5-b
readonly REMOTE_TEST_RUN_ID=87654321
readonly REMOTE_TEST_RUN_ATTEMPT=4
REMOTE_TEST_STAGE="$(mktemp -d "/tmp/scribe-production-browser-${REMOTE_TEST_RUN_ID}-${REMOTE_TEST_RUN_ATTEMPT}.XXXXXXXXXX")"
readonly REMOTE_TEST_STAGE
readonly REMOTE_TEST_STATE="${REMOTE_TEST_STAGE}/storage-state.json"
export TEST_RUN_ID TEST_RUN_ATTEMPT TEST_JOB TEST_SECRET TEST_PROJECT TEST_PROJECT_NUMBER TEST_REGION TEST_ZONE

cleanup() {
  rm -f -- "$REMOTE_TEST_STATE"
  rmdir -- "$REMOTE_TEST_STAGE" >/dev/null 2>&1 || true
  rm -rf -- "$TEST_DIR"
}
trap cleanup EXIT
mkdir -p "$TEST_DIR/bin" "$TEST_DIR/remote-bin" "$TEST_DIR/artifacts" \
  "$TEST_DIR/tmp" "$TEST_DIR/remote" "$TEST_DIR/container"

fail() {
  printf 'Production browser readiness transport contract failed: %s\n' "$*" >&2
  exit 1
}

[[ -x "$ROOT_DIR/ci/run-production-browser-readiness.sh" ]] ||
  fail "production transport is not executable"
[[ -x "$ROOT_DIR/ci/run-production-browser-session-remote.sh" ]] ||
  fail "production remote helper is not executable"

session_value='AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'
session_expires="$(( $(date -u +%s) + 2700 ))"
jq -cn \
  --arg value "$session_value" \
  --argjson expires "$session_expires" '
    {
      cookies: [{
        name: "scribe_session",
        value: $value,
        domain: "scribe-123456789.us-east5.run.app",
        path: "/",
        expires: $expires,
        httpOnly: true,
        secure: true,
        sameSite: "Lax"
      }],
      origins: []
    }
  ' >"$TEST_DIR/state-fixture.json"
chmod 0600 "$TEST_DIR/state-fixture.json"
read -r expected_state_sha _ < <(sha256sum "$TEST_DIR/state-fixture.json")
short_session_expires="$(( $(date -u +%s) + 2000 ))"
jq --argjson expires "$short_session_expires" \
  '.cookies[0].expires = $expires' \
  "$TEST_DIR/state-fixture.json" >"$TEST_DIR/short-state-fixture.json"
chmod 0600 "$TEST_DIR/short-state-fixture.json"

# Exercise the copied VM helper directly with a fake Compose boundary before
# testing the IAP/Secret Manager transport around it.
cat >"$TEST_DIR/remote-bin/readlink" <<'FAKE_READLINK'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" == "-f -- /mnt/disks/data/scribe/prod" ]]; then
  printf '%s\n' '/mnt/disks/data/scribe/prod'
  exit 0
fi
exec /usr/bin/readlink "$@"
FAKE_READLINK

cat >"$TEST_DIR/remote-bin/stat" <<'FAKE_STAT'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" == "-c %F -- /mnt/disks/data/scribe/prod" ]]; then
  printf '%s\n' 'directory'
  exit 0
fi
exec /usr/bin/stat "$@"
FAKE_STAT

cat >"$TEST_DIR/remote-bin/docker" <<'FAKE_DOCKER'
#!/usr/bin/env bash
set -euo pipefail

joined=" $* "
container_state="$TEST_DIR/container/remote-helper-state.json"
printf '%s\n' "remote-docker $joined" >>"$MOCK_REMOTE_EVENT_LOG"

if [[ "$joined" == *" /app/scribe-browser-session --output /tmp/scribe-browser-session-"* ]]; then
  install -m 0600 "$TEST_STATE_FIXTURE" "$container_state"
  printf '%s\n' 'helper-mint' >>"$MOCK_REMOTE_EVENT_LOG"
  exit 0
fi
if [[ "$joined" == *" cp api:/tmp/scribe-browser-session-"* ]]; then
  [[ "${REMOTE_DOCKER_FAIL:-}" != "cp" ]] || exit 27
  destination="${@: -1}"
  install -m 0600 "$container_state" "$destination"
  printf '%s\n' 'helper-copy' >>"$MOCK_REMOTE_EVENT_LOG"
  exit 0
fi
if [[ "$joined" == *" exec -T api rm -f -- /tmp/scribe-browser-session-"* ]]; then
  rm -f -- "$container_state"
  printf '%s\n' 'helper-container-remove' >>"$MOCK_REMOTE_EVENT_LOG"
  exit 0
fi
exit 98
FAKE_DOCKER
chmod 0755 "$TEST_DIR/remote-bin/readlink" "$TEST_DIR/remote-bin/stat" "$TEST_DIR/remote-bin/docker"

export TEST_DIR
export TEST_STATE_FIXTURE="$TEST_DIR/state-fixture.json"
export MOCK_REMOTE_EVENT_LOG="$TEST_DIR/remote-helper-events.log"

: >"$MOCK_REMOTE_EVENT_LOG"
PATH="$TEST_DIR/remote-bin:$PATH" \
  "$ROOT_DIR/ci/run-production-browser-session-remote.sh" \
  mint "$REMOTE_TEST_RUN_ID" "$REMOTE_TEST_RUN_ATTEMPT" "$REMOTE_TEST_STAGE" \
  >"$TEST_DIR/remote-helper.out" 2>"$TEST_DIR/remote-helper.err"
[[ -f "$REMOTE_TEST_STATE" && ! -L "$REMOTE_TEST_STATE" ]] ||
  fail "remote helper did not leave one regular host state file"
[[ "$(stat -c '%a' "$REMOTE_TEST_STATE")" == 600 ]] ||
  fail "remote helper host state is not mode 0600"
cmp -s "$TEST_DIR/state-fixture.json" "$REMOTE_TEST_STATE" ||
  fail "remote helper altered the storage state"
[[ ! -e "$TEST_DIR/container/remote-helper-state.json" ]] ||
  fail "remote helper retained the container state"
mint_line="$(grep -n '^helper-mint$' "$MOCK_REMOTE_EVENT_LOG" | cut -d: -f1)"
copy_line="$(grep -n '^helper-copy$' "$MOCK_REMOTE_EVENT_LOG" | cut -d: -f1)"
remove_line="$(grep -n '^helper-container-remove$' "$MOCK_REMOTE_EVENT_LOG" | awk -F: -v copy="$copy_line" '$1 > copy {print $1; exit}')"
[[ "$mint_line" -lt "$copy_line" && "$copy_line" -lt "$remove_line" ]] ||
  fail "remote helper did not remove the container credential immediately after copying it"

PATH="$TEST_DIR/remote-bin:$PATH" \
  "$ROOT_DIR/ci/run-production-browser-session-remote.sh" \
  cleanup "$REMOTE_TEST_RUN_ID" "$REMOTE_TEST_RUN_ATTEMPT" "$REMOTE_TEST_STAGE" \
  >"$TEST_DIR/remote-cleanup.out" 2>"$TEST_DIR/remote-cleanup.err"
[[ ! -e "$REMOTE_TEST_STATE" && ! -L "$REMOTE_TEST_STATE" ]] ||
  fail "remote helper cleanup retained the host state"

: >"$MOCK_REMOTE_EVENT_LOG"
set +e
PATH="$TEST_DIR/remote-bin:$PATH" \
  REMOTE_DOCKER_FAIL=cp \
  "$ROOT_DIR/ci/run-production-browser-session-remote.sh" \
  mint "$REMOTE_TEST_RUN_ID" "$REMOTE_TEST_RUN_ATTEMPT" "$REMOTE_TEST_STAGE" \
  >"$TEST_DIR/remote-failure.out" 2>"$TEST_DIR/remote-failure.err"
remote_failure_status=$?
set -e
[[ "$remote_failure_status" -eq 2 ]] || fail "remote copy failure was not categorical"
[[ ! -e "$REMOTE_TEST_STATE" && ! -L "$REMOTE_TEST_STATE" ]] ||
  fail "remote copy failure retained host state"
[[ ! -e "$TEST_DIR/container/remote-helper-state.json" ]] ||
  fail "remote copy failure retained container state"

cat >"$TEST_DIR/bin/timeout" <<'FAKE_TIMEOUT'
#!/usr/bin/env bash
set -euo pipefail

if [[ "${4:-}" == gcloud && "${5:-}" == secrets ]]; then
  [[ "$1" == "--signal=TERM" && "$2" == "--kill-after=5s" && "$3" == 30s ]]
  export MOCK_SECRET_TIMEOUT_GUARD=true
elif [[ "${4:-}" == gcloud && "${5:-} ${6:-} ${7:-}" == "run jobs update" ]]; then
  [[ "$1" == "--signal=TERM" && "$2" == "--kill-after=5s" && "$3" == 180s ]]
  export MOCK_JOB_TIMEOUT_GUARD=true
elif [[ "${4:-}" == gcloud && "${5:-} ${6:-} ${7:-}" == "run jobs describe" ]]; then
  [[ "$1" == "--signal=TERM" && "$2" == "--kill-after=5s" && "$3" == 30s ]]
  export MOCK_JOB_TIMEOUT_GUARD=true
elif [[ "${4:-}" == gcloud && "${5:-} ${6:-}" == "compute ssh" ]] ||
  [[ "${4:-}" == gcloud && "${5:-} ${6:-}" == "compute scp" ]]; then
  [[ "$1" == "--signal=TERM" && "$2" == "--kill-after=5s" && "$3" == 180s ]]
  export MOCK_REMOTE_TIMEOUT_GUARD=true
fi
exec /usr/bin/timeout "$@"
FAKE_TIMEOUT
chmod 0755 "$TEST_DIR/bin/timeout"

cat >"$TEST_DIR/bin/gcloud" <<'FAKE_GCLOUD'
#!/usr/bin/env bash
set -euo pipefail

if [[ "${1:-}" == secrets ]]; then
  [[ "${MOCK_SECRET_TIMEOUT_GUARD:-}" == true ]]
elif [[ "${1:-} ${2:-} ${3:-}" == "run jobs update" ||
  "${1:-} ${2:-} ${3:-}" == "run jobs describe" ]]; then
  [[ "${MOCK_JOB_TIMEOUT_GUARD:-}" == true ]]
elif [[ "${1:-} ${2:-}" == "compute ssh" || "${1:-} ${2:-}" == "compute scp" ]]; then
  [[ "${MOCK_REMOTE_TIMEOUT_GUARD:-}" == true ]]
fi

printf '%q ' "$@" >>"$MOCK_GCLOUD_LOG"
printf '\n' >>"$MOCK_GCLOUD_LOG"
printf '%q ' "$@" >>"$MOCK_ALL_GCLOUD_LOG"
printf '\n' >>"$MOCK_ALL_GCLOUD_LOG"

assert_common() {
  local kind="$1" joined key_file="" known_hosts="" argument
  shift
  joined=" $* "
  [[ "$joined" == *" --project=$TEST_PROJECT "* ]]
  [[ "$joined" == *" --zone=$TEST_ZONE "* ]]
  [[ "$joined" == *" --tunnel-through-iap "* ]]
  [[ "$joined" == *" --ssh-key-file="* ]]
  [[ "$joined" == *" --ssh-key-expire-after=50m "* ]]
  for argument in "$@"; do
    case "$argument" in
      --ssh-key-file=*) key_file="${argument#--ssh-key-file=}" ;;
      --ssh-flag=-o\ UserKnownHostsFile=* | --scp-flag=-o\ UserKnownHostsFile=*)
        known_hosts="${argument#*UserKnownHostsFile=}" ;;
    esac
  done
  [[ -f "$key_file" && ! -L "$key_file" && "$(stat -c '%a' "$key_file")" == 600 ]]
  [[ -f "${key_file}.pub" && ! -L "${key_file}.pub" && "$(stat -c '%a' "${key_file}.pub")" == 600 ]]
  [[ -f "$known_hosts" && ! -L "$known_hosts" && "$(stat -c '%a' "$known_hosts")" == 600 ]]
  [[ "$(stat -c '%a' "$(dirname "$known_hosts")")" == 700 ]]
  if [[ "$kind" == ssh ]]; then
    [[ "$joined" == *" --ssh-flag=-o StrictHostKeyChecking=accept-new "* ]]
    [[ "$joined" == *" --ssh-flag=-o IdentitiesOnly=yes "* ]]
    [[ "$joined" == *" --ssh-flag=-o ConnectTimeout=30 "* ]]
    [[ "$joined" == *" --ssh-flag=-o ServerAliveInterval=15 "* ]]
    [[ "$joined" == *" --ssh-flag=-o ServerAliveCountMax=4 "* ]]
  else
    [[ "$joined" == *" --scp-flag=-o StrictHostKeyChecking=accept-new "* ]]
    [[ "$joined" == *" --scp-flag=-o IdentitiesOnly=yes "* ]]
    [[ "$joined" == *" --scp-flag=-o ConnectTimeout=30 "* ]]
    [[ "$joined" == *" --scp-flag=-o ServerAliveInterval=15 "* ]]
    [[ "$joined" == *" --scp-flag=-o ServerAliveCountMax=4 "* ]]
  fi
}

emit_job_json() {
  local version="$1"
  jq -cn \
    --arg name "projects/${TEST_PROJECT_NUMBER}/locations/${TEST_REGION}/jobs/${TEST_JOB}" \
    --arg secret "$TEST_SECRET" \
    --arg version "$version" '
      {
        metadata: {name: $name},
        spec: {template: {spec: {template: {spec: {containers: [{env: [{
          name: "SCRIBE_BROWSER_STORAGE_STATE_JSON",
          valueFrom: {secretKeyRef: {name: $secret, key: $version}}
        }]}]}}}}}
      }
    '
}

emit_job_v2_json() {
  local version="$1"
  jq -cn \
    --arg name "projects/${TEST_PROJECT_NUMBER}/locations/${TEST_REGION}/jobs/${TEST_JOB}" \
    --arg secret "projects/${TEST_PROJECT_NUMBER}/secrets/${TEST_SECRET}" \
    --arg version "$version" '
      {
        name: $name,
        template: {template: {containers: [{env: [{
          name: "SCRIBE_BROWSER_STORAGE_STATE_JSON",
          valueSource: {secretKeyRef: {secret: $secret, version: $version}}
        }]}]}}
      }
    '
}

if [[ "$1 $2 $3" == "run jobs update" ]]; then
  expected=""
  for argument in "$@"; do
    case "$argument" in
      --update-secrets=SCRIBE_BROWSER_STORAGE_STATE_JSON=*)
        expected="${argument#--update-secrets=SCRIBE_BROWSER_STORAGE_STATE_JSON=}" ;;
    esac
  done
  [[ "$*" == *"--project $TEST_PROJECT"* && "$*" == *"--region $TEST_REGION"* ]]
  [[ "$*" == *"--format=json"* && "$*" == *"--quiet"* ]]
  [[ "$expected" =~ ^${TEST_SECRET}:([1-9][0-9]*)$ ]]
  version="${BASH_REMATCH[1]}"
  printf '%s\n' "job-update-${version}" >>"$MOCK_EVENT_LOG"
  credential_version=""
  [[ ! -e "$MOCK_CREDENTIAL_VERSION_FILE" ]] || credential_version="$(<"$MOCK_CREDENTIAL_VERSION_FILE")"
  if [[ "$version" == 1 && -n "$credential_version" &&
    ("${MOCK_FAIL_STAGE:-}" == cleanup-job-restore || "${MOCK_FAIL_STAGE:-}" == readiness-and-cleanup) ]]; then
    exit 52
  fi
  printf '%s\n' "$version" >"$MOCK_JOB_VERSION_FILE"
  if [[ "$version" != 1 && "${MOCK_FAIL_STAGE:-}" == job-update-credential-ambiguous ]]; then
    exit 124
  fi
  if [[ "$version" != 1 && "${MOCK_FAIL_STAGE:-}" == job-update-credential ]]; then
    printf '%s\n' 1 >"$MOCK_JOB_VERSION_FILE"
    exit 51
  fi
  emit_job_json "$version"
  exit 0
fi

if [[ "$1 $2 $3" == "run jobs describe" ]]; then
  [[ "$4" == "$TEST_JOB" ]]
  version="$(<"$MOCK_JOB_VERSION_FILE")"
  printf '%s\n' "job-describe-${version}" >>"$MOCK_EVENT_LOG"
  if [[ "$version" != 1 && "${MOCK_FAIL_STAGE:-}" == job-attest-credential ]]; then
    emit_job_json 1
  else
    emit_job_v2_json "$version"
  fi
  exit 0
fi

if [[ "$1 $2" == "compute ssh" ]]; then
  [[ "$3" == "cloud-compose@scribe" ]]
  assert_common ssh "$@"
  command_value=""
  for argument in "$@"; do
    case "$argument" in --command=*) command_value="${argument#--command=}" ;; esac
  done
  [[ -n "$command_value" ]]
  if [[ "$command_value" == *"mktemp -d --"* ]]; then
    printf '%s\n' 'remote-stage' >>"$MOCK_EVENT_LOG"
    [[ "${MOCK_FAIL_STAGE:-}" != remote-stage-create ]] || exit 41
    mkdir -p "$MOCK_REMOTE_STAGE"
    chmod 0700 "$MOCK_REMOTE_STAGE"
    printf '%s\n' "$MOCK_REMOTE_STAGE_VIRTUAL"
    exit 0
  fi
  if [[ "$command_value" == *" mint "* ]]; then
    printf '%s\n' 'remote-mint' >>"$MOCK_EVENT_LOG"
    touch "$MOCK_REMOTE_CONTAINER"
    [[ "${MOCK_FAIL_STAGE:-}" != remote-mint ]] || exit 31
    [[ -f "$MOCK_REMOTE_HELPER" && ! -L "$MOCK_REMOTE_HELPER" ]]
    [[ "$command_value" == *"sha256sum -- '$MOCK_REMOTE_STAGE_VIRTUAL/helper.sh'"* ]]
    [[ "$command_value" == *"'$EXPECTED_REMOTE_HELPER_SHA'"* ]]
    actual_helper_sha="$(sha256sum "$MOCK_REMOTE_HELPER" | cut -d' ' -f1)"
    [[ "$actual_helper_sha" == "$EXPECTED_REMOTE_HELPER_SHA" ]]
    chmod 0700 "$MOCK_REMOTE_HELPER"
    install -m 0600 "$TEST_STATE_FIXTURE" "$MOCK_REMOTE_STATE"
    printf '%s\n' 'container-copy' >>"$MOCK_EVENT_LOG"
    rm -f "$MOCK_REMOTE_CONTAINER"
    printf '%s\n' 'container-remove' >>"$MOCK_EVENT_LOG"
    exit 0
  fi
  [[ "$command_value" != *"bash "* && "$command_value" != *" cleanup "* && "$command_value" != *"cat "* ]]
  if [[ "$command_value" == *"exec -T api sh -ceu"* ]]; then
    [[ "$command_value" == *"/tmp/scribe-browser-session-*.json"* ]]
    rm -f "$MOCK_STALE_REMOTE_CONTAINER"
    touch "$MOCK_CONTAINER_SWEEPED"
  fi
  printf '%s\n' 'remote-cleanup' >>"$MOCK_EVENT_LOG"
  rm -f "$MOCK_REMOTE_CONTAINER" "$MOCK_STALE_REMOTE_CONTAINER" "$MOCK_REMOTE_STATE" "$MOCK_REMOTE_HELPER" \
    "$MOCK_LEGACY_REMOTE_HELPER" "$MOCK_LEGACY_REMOTE_STATE" \
    "$MOCK_STALE_REMOTE_HELPER" "$MOCK_STALE_REMOTE_STATE"
  rmdir "$MOCK_REMOTE_STAGE" "$MOCK_STALE_REMOTE_STAGE" >/dev/null 2>&1 || true
  [[ "${MOCK_FAIL_STAGE:-}" != remote-cleanup ]] || exit 32
  exit 0
fi

if [[ "$1 $2" == "compute scp" ]]; then
  assert_common scp "$@"
  source_path="$3"
  target_path="$4"
  if [[ "$source_path" != cloud-compose@scribe:* ]]; then
    [[ "$target_path" == "cloud-compose@scribe:${MOCK_REMOTE_STAGE_VIRTUAL}/helper.sh" ]]
    [[ -f "$source_path" && ! -L "$source_path" && "$(stat -c '%a' "$source_path")" == 700 ]]
    cmp -s "$source_path" "$REMOTE_HELPER_SOURCE"
    install -m 0700 "$source_path" "$MOCK_REMOTE_HELPER"
    printf '%s\n' 'helper-copy' >>"$MOCK_EVENT_LOG"
    [[ "${MOCK_FAIL_STAGE:-}" != helper-copy ]] || exit 33
    exit 0
  fi
  [[ "$source_path" == "cloud-compose@scribe:${MOCK_REMOTE_STAGE_VIRTUAL}/storage-state.json" ]]
  [[ -f "$MOCK_REMOTE_STATE" && ! -L "$MOCK_REMOTE_STATE" && "$(stat -c '%a' "$MOCK_REMOTE_STATE")" == 600 ]]
  case "${MOCK_FAIL_STAGE:-}" in
    state-scp) exit 34 ;;
    state-symlink) ln -s "$TEST_STATE_FIXTURE" "$target_path" ;;
    state-mode) install -m 0644 "$MOCK_REMOTE_STATE" "$target_path" ;;
    state-invalid)
      install -m 0600 /dev/null "$target_path"
      printf '%0130d\n' 0 >"$target_path"
      ;;
    state-short-expiry) install -m 0600 "$TEST_SHORT_STATE_FIXTURE" "$target_path" ;;
    *) install -m 0600 "$MOCK_REMOTE_STATE" "$target_path" ;;
  esac
  printf '%s\n' 'state-scp' >>"$MOCK_EVENT_LOG"
  exit 0
fi

emit_version_leaf() {
  local version="$1"
  printf '%s\n' "$version"
}

next_version() {
  local version
  version="$(( $(<"$MOCK_LAST_VERSION_FILE") + 1 ))"
  printf '%s\n' "$version" >"$MOCK_LAST_VERSION_FILE"
  printf '%s\n' ENABLED >"$MOCK_VERSION_DIR/$version"
  printf '%s' "$version"
}

if [[ "$1 $2 $3" == "secrets versions add" ]]; then
  data_file=""
  for argument in "$@"; do
    case "$argument" in --data-file=*) data_file="${argument#--data-file=}" ;; esac
  done
  [[ "$4" == "$TEST_SECRET" ]]
  [[ "$*" == *"--project=$TEST_PROJECT"* && "$*" == *"--format=value(name.basename())"* ]]
  [[ -f "$data_file" && ! -L "$data_file" && "$(stat -c '%a' "$data_file")" == 600 ]]
  cmp -s "$data_file" "$TEST_STATE_FIXTURE"
  install -m 0600 "$data_file" "$MOCK_SECRET_CAPTURE"
  printf '%s\n' 'secret-add' >>"$MOCK_EVENT_LOG"
  actual_version="$(next_version)"
  printf '%s\n' "$actual_version" >"$MOCK_CREDENTIAL_VERSION_FILE"
  case "${MOCK_FAIL_STAGE:-}" in
    secret-add-ambiguous | secret-add-ambiguous-unobserved) exit 124 ;;
    secret-add-ambiguous-identified)
      emit_version_leaf "$actual_version"
      exit 124
      ;;
    secret-add-malformed)
      printf '%s\n' unexpected-version-identity
      exit 0
      ;;
    secret-add-term)
      touch "$MOCK_SECRET_ADD_MARKER"
      while [[ ! -e "$MOCK_SECRET_ADD_RELEASE" ]]; do sleep 0.02; done
      emit_version_leaf "$actual_version"
      exit 0
      ;;
  esac
  emit_version_leaf "$actual_version"
  exit 0
fi

if [[ "$1 $2 $3" == "secrets versions describe" ]]; then
  version="$4"
  [[ "$*" == *"--secret=$TEST_SECRET"* && "$*" == *"--project=$TEST_PROJECT"* && "$*" == *"--format=json"* ]]
  [[ -f "$MOCK_VERSION_DIR/$version" ]]
  state="$(<"$MOCK_VERSION_DIR/$version")"
  if [[ "${MOCK_FAIL_STAGE:-}" == secret-attest && "$version" != 1 && "$state" == ENABLED ]]; then
    state=DISABLED
  fi
  if [[ "${MOCK_FAIL_STAGE:-}" == secret-destroy-attest-lag && "$state" == DESTROYED ]]; then
    count=0
    [[ ! -e "$MOCK_DESTROY_DESCRIBE_COUNT" ]] || count="$(<"$MOCK_DESTROY_DESCRIBE_COUNT")"
    count=$((count + 1))
    printf '%s\n' "$count" >"$MOCK_DESTROY_DESCRIBE_COUNT"
    ((count >= 3)) || state=ENABLED
  fi
  printf '%s\n' "secret-describe-${version}-${state}" >>"$MOCK_EVENT_LOG"
  printf '{"name":"projects/%s/secrets/%s/versions/%s","state":"%s"}\n' \
    "$TEST_PROJECT_NUMBER" "$TEST_SECRET" "$version" "$state"
  exit 0
fi

if [[ "$1 $2 $3" == "secrets versions destroy" ]]; then
  version="$4"
  [[ "$version" != 1 && -f "$MOCK_VERSION_DIR/$version" ]]
  printf '%s\n' "secret-destroy-${version}" >>"$MOCK_EVENT_LOG"
  credential_version=""
  [[ ! -e "$MOCK_CREDENTIAL_VERSION_FILE" ]] || credential_version="$(<"$MOCK_CREDENTIAL_VERSION_FILE")"
  if [[ "${MOCK_FAIL_STAGE:-}" == secret-destroy-once && "$version" == "$credential_version" &&
    ! -e "$MOCK_DESTROY_ATTEMPT" ]]; then
    touch "$MOCK_DESTROY_ATTEMPT"
    exit 36
  fi
  if [[ "${MOCK_FAIL_STAGE:-}" == secret-destroy-always && "$version" == "$credential_version" ]]; then
    exit 36
  fi
  printf '%s\n' DESTROYED >"$MOCK_VERSION_DIR/$version"
  [[ "${MOCK_FAIL_STAGE:-}" != secret-destroy-ambiguous ]] || exit 36
  exit 0
fi

if [[ "$1 $2 $3" == "secrets versions list" ]]; then
  [[ "$4" == "$TEST_SECRET" ]]
  [[ "$*" == *"--filter=state!=DESTROYED"* && "$*" == *"--format=value(name.basename(),state)"* ]]
  printf '%s\n' 'version-list' >>"$MOCK_EVENT_LOG"
  [[ "${MOCK_FAIL_STAGE:-}" != version-list ]] || exit 46
  while IFS= read -r version; do
    state="$(<"$MOCK_VERSION_DIR/$version")"
    [[ "$state" != DESTROYED ]] || continue
    if [[ "${MOCK_FAIL_STAGE:-}" == secret-add-ambiguous-unobserved &&
      -e "$MOCK_CREDENTIAL_VERSION_FILE" && "$version" == "$(<"$MOCK_CREDENTIAL_VERSION_FILE")" ]]; then
      continue
    fi
    if [[ "${MOCK_FAIL_STAGE:-}" == version-list-omits-known &&
      -e "$MOCK_CREDENTIAL_VERSION_FILE" && "$version" == "$(<"$MOCK_CREDENTIAL_VERSION_FILE")" ]]; then
      continue
    fi
    printf '%s\t%s\n' "$version" "$state"
  done < <(find "$MOCK_VERSION_DIR" -mindepth 1 -maxdepth 1 -type f -printf '%f\n' | sort -n)
  exit 0
fi

if [[ "$1 $2 $3 $4" == "run jobs executions list" ]]; then
  [[ "$5" == --job && "$6" == "$TEST_JOB" ]]
  printf '%s\n' 'readiness-preflight' >>"$MOCK_EVENT_LOG"
  [[ "${MOCK_FAIL_STAGE:-}" != browser-preflight ]] || exit 47
  printf '%s\n' '[]'
  exit 0
fi

if [[ "$1 $2 $3" == "run jobs execute" ]]; then
  [[ "$4" == "$TEST_JOB" ]]
  credential_version="$(<"$MOCK_CREDENTIAL_VERSION_FILE")"
  [[ "$(<"$MOCK_JOB_VERSION_FILE")" == "$credential_version" ]]
  [[ "$*" == *"--update-env-vars=SCRIBE_BROWSER_EXPECTED_SECRET_VERSION=$credential_version,SCRIBE_BROWSER_EXPECTED_STORAGE_STATE_SHA256=$EXPECTED_STATE_SHA,SCRIBE_READINESS_EXECUTION_ID="* ]]
  [[ ! -e "$MOCK_LOCAL_STATE" && ! -e "$MOCK_REMOTE_STATE" && ! -e "$MOCK_REMOTE_HELPER" && ! -e "$MOCK_REMOTE_CONTAINER" ]]
  printf '%s\n' 'readiness' >>"$MOCK_EVENT_LOG"
  printf '%s\n' "${TEST_JOB}-abc12"
  exit 0
fi

if [[ "$1 $2 $3 $4" == "run jobs executions describe" ]]; then
  [[ "$5" == "${TEST_JOB}-abc12" ]]
  printf '%s\n' 'readiness-describe' >>"$MOCK_EVENT_LOG"
  if [[ "${MOCK_FAIL_STAGE:-}" == readiness || "${MOCK_FAIL_STAGE:-}" == readiness-and-cleanup ]]; then
    printf '{"metadata":{"name":"projects/%s/locations/%s/jobs/%s/executions/%s-abc12"},"status":{"conditions":[{"type":"Completed","status":"False","reason":"Failed"}],"failedCount":1}}\n' \
      "$TEST_PROJECT_NUMBER" "$TEST_REGION" "$TEST_JOB" "$TEST_JOB"
  else
    printf '{"metadata":{"name":"projects/%s/locations/%s/jobs/%s/executions/%s-abc12"},"status":{"conditions":[{"type":"Completed","status":"True"}]}}\n' \
      "$TEST_PROJECT_NUMBER" "$TEST_REGION" "$TEST_JOB" "$TEST_JOB"
  fi
  exit 0
fi

printf '%s\n' 'unexpected fake gcloud invocation' >&2
exit 99
FAKE_GCLOUD
chmod 0755 "$TEST_DIR/bin/gcloud"

export PATH="$TEST_DIR/bin:$PATH"
export REMOTE_HELPER_SOURCE="$ROOT_DIR/ci/run-production-browser-session-remote.sh"
export MOCK_GCLOUD_LOG="$TEST_DIR/gcloud.log"
export MOCK_ALL_GCLOUD_LOG="$TEST_DIR/all-gcloud.log"
export MOCK_EVENT_LOG="$TEST_DIR/events.log"
export MOCK_REMOTE_STAGE_VIRTUAL="/tmp/scribe-production-browser-${TEST_RUN_ID}-${TEST_RUN_ATTEMPT}.AbCdEf1234"
export MOCK_REMOTE_STAGE="$TEST_DIR/remote/current-stage"
export MOCK_REMOTE_HELPER="$MOCK_REMOTE_STAGE/helper.sh"
export MOCK_REMOTE_STATE="$MOCK_REMOTE_STAGE/storage-state.json"
export MOCK_LEGACY_REMOTE_HELPER="$TEST_DIR/remote/legacy-helper.sh"
export MOCK_LEGACY_REMOTE_STATE="$TEST_DIR/remote/legacy-state.json"
export MOCK_STALE_REMOTE_STAGE="$TEST_DIR/remote/stale-stage"
export MOCK_STALE_REMOTE_HELPER="$MOCK_STALE_REMOTE_STAGE/helper.sh"
export MOCK_STALE_REMOTE_STATE="$MOCK_STALE_REMOTE_STAGE/storage-state.json"
export MOCK_REMOTE_CONTAINER="$TEST_DIR/container/state.json"
export MOCK_STALE_REMOTE_CONTAINER="$TEST_DIR/container/stale-state.json"
export MOCK_CONTAINER_SWEEPED="$TEST_DIR/container-sweeped"
export MOCK_SECRET_CAPTURE="$TEST_DIR/secret-capture.json"
export MOCK_DESTROY_ATTEMPT="$TEST_DIR/destroy-attempt"
export MOCK_DESTROY_DESCRIBE_COUNT="$TEST_DIR/destroy-describe-count"
export MOCK_VERSION_DIR="$TEST_DIR/versions"
export MOCK_LAST_VERSION_FILE="$TEST_DIR/last-version"
export MOCK_CREDENTIAL_VERSION_FILE="$TEST_DIR/credential-version"
export MOCK_JOB_VERSION_FILE="$TEST_DIR/job-version"
export MOCK_SECRET_ADD_MARKER="$TEST_DIR/secret-add-marker"
export MOCK_SECRET_ADD_RELEASE="$TEST_DIR/secret-add-release"
export MOCK_LOCAL_STATE="$TEST_DIR/tmp/scribe-production-browser.UNSET/storage-state.json"
export TEST_SHORT_STATE_FIXTURE="$TEST_DIR/short-state-fixture.json"
export EXPECTED_STATE_SHA="$expected_state_sha"
read -r expected_remote_helper_sha _ < <(sha256sum "$REMOTE_HELPER_SOURCE")
export EXPECTED_REMOTE_HELPER_SHA="$expected_remote_helper_sha"
: >"$MOCK_ALL_GCLOUD_LOG"

prepare_case() {
  local last_version="${1:-40}" seed_history="${2:-true}"

  rm -f -- "$MOCK_REMOTE_HELPER" "$MOCK_REMOTE_STATE" "$MOCK_REMOTE_CONTAINER" \
    "$MOCK_STALE_REMOTE_CONTAINER" "$MOCK_CONTAINER_SWEEPED" "$MOCK_CREDENTIAL_VERSION_FILE" \
    "$MOCK_SECRET_CAPTURE" "$MOCK_DESTROY_ATTEMPT" "$MOCK_DESTROY_DESCRIBE_COUNT" \
    "$MOCK_SECRET_ADD_MARKER" "$MOCK_SECRET_ADD_RELEASE" "$TEST_DIR/planted-helper-executed"
  rm -rf -- "$MOCK_REMOTE_STAGE" "$MOCK_STALE_REMOTE_STAGE" "$MOCK_VERSION_DIR"
  mkdir -p "$MOCK_STALE_REMOTE_STAGE" "$MOCK_VERSION_DIR"
  chmod 0700 "$MOCK_STALE_REMOTE_STAGE"
  # shellcheck disable=SC2016 # Planted helpers retain literal test paths if executed.
  printf '%s\n' '#!/usr/bin/env bash' 'touch "$TEST_DIR/planted-helper-executed"' >"$MOCK_LEGACY_REMOTE_HELPER"
  # shellcheck disable=SC2016 # Planted helpers retain literal test paths if executed.
  printf '%s\n' '#!/usr/bin/env bash' 'touch "$TEST_DIR/planted-helper-executed"' >"$MOCK_STALE_REMOTE_HELPER"
  chmod 0700 "$MOCK_LEGACY_REMOTE_HELPER" "$MOCK_STALE_REMOTE_HELPER"
  install -m 0600 "$TEST_STATE_FIXTURE" "$MOCK_LEGACY_REMOTE_STATE"
  install -m 0600 "$TEST_STATE_FIXTURE" "$MOCK_STALE_REMOTE_STATE"
  printf '%s\n' ENABLED >"$MOCK_VERSION_DIR/1"
  if [[ "$seed_history" == true ]]; then
    printf '%s\n' ENABLED >"$MOCK_VERSION_DIR/2"
    printf '%s\n' ENABLED >"$MOCK_VERSION_DIR/$last_version"
  fi
  printf '%s\n' "$last_version" >"$MOCK_LAST_VERSION_FILE"
  printf '%s\n' 99 >"$MOCK_JOB_VERSION_FILE"
  install -m 0600 "$TEST_STATE_FIXTURE" "$MOCK_STALE_REMOTE_CONTAINER"
  : >"$MOCK_GCLOUD_LOG"
  : >"$MOCK_EVENT_LOG"
}

invoke_transport() {
  local case_name="$1" fail_stage="$2"

  GCLOUD_PROJECT="$TEST_PROJECT" \
  SCRIBE_DEPLOYMENT_ENVIRONMENT=production \
  SCRIBE_REGION="$TEST_REGION" \
  SCRIBE_ZONE="$TEST_ZONE" \
  SCRIBE_INSTANCE=scribe \
  GITHUB_RUN_ID="$TEST_RUN_ID" \
  GITHUB_RUN_ATTEMPT="$TEST_RUN_ATTEMPT" \
  RUNNER_TEMP="$TEST_DIR/tmp" \
  MOCK_FAIL_STAGE="$fail_stage" \
    "$ROOT_DIR/ci/run-production-browser-readiness.sh" \
      "$TEST_JOB" "$TEST_SECRET" "$TEST_DIR/artifacts/${case_name}.log" \
      >"$TEST_DIR/${case_name}.out" 2>"$TEST_DIR/${case_name}.err"
}

run_transport() {
  local case_name="$1" fail_stage="${2:-}" last_version="${3:-40}" seed_history="${4:-true}"
  local attempt transport_pid

  prepare_case "$last_version" "$seed_history"
  rm -f -- "$TEST_DIR/artifacts/${case_name}.log"
  set +e
  if [[ "$fail_stage" == secret-add-term ]]; then
    GCLOUD_PROJECT="$TEST_PROJECT" \
    SCRIBE_DEPLOYMENT_ENVIRONMENT=production \
    SCRIBE_REGION="$TEST_REGION" \
    SCRIBE_ZONE="$TEST_ZONE" \
    SCRIBE_INSTANCE=scribe \
    GITHUB_RUN_ID="$TEST_RUN_ID" \
    GITHUB_RUN_ATTEMPT="$TEST_RUN_ATTEMPT" \
    RUNNER_TEMP="$TEST_DIR/tmp" \
    MOCK_FAIL_STAGE="$fail_stage" \
      "$ROOT_DIR/ci/run-production-browser-readiness.sh" \
        "$TEST_JOB" "$TEST_SECRET" "$TEST_DIR/artifacts/${case_name}.log" \
        >"$TEST_DIR/${case_name}.out" 2>"$TEST_DIR/${case_name}.err" &
    transport_pid=$!
    set -e
    [[ "$transport_pid" =~ ^[1-9][0-9]*$ ]] || fail "TERM test did not capture an exact transport PID"
    for ((attempt = 1; attempt <= 250; attempt++)); do
      [[ ! -e "$MOCK_SECRET_ADD_MARKER" ]] || break
      kill -0 "$transport_pid" >/dev/null 2>&1 || break
      sleep 0.02
    done
    [[ -e "$MOCK_SECRET_ADD_MARKER" ]] || fail "TERM test did not reach credential creation"
    kill -TERM "$transport_pid"
    touch "$MOCK_SECRET_ADD_RELEASE"
    set +e
    wait "$transport_pid"
    TRANSPORT_STATUS=$?
  else
    invoke_transport "$case_name" "$fail_stage"
    TRANSPORT_STATUS=$?
  fi
  set -e
  [[ "$TRANSPORT_STATUS" -ge 0 ]]
}

event_line() {
  local event="$1"
  grep -n -m1 "^${event}$" "$MOCK_EVENT_LOG" | cut -d: -f1
}

assert_only_placeholder_active() {
  local active
  active="$(while IFS= read -r version; do
    [[ "$(<"$MOCK_VERSION_DIR/$version")" == DESTROYED ]] || printf '%s\n' "$version"
  done < <(find "$MOCK_VERSION_DIR" -mindepth 1 -maxdepth 1 -type f -printf '%f\n' | sort -n))"
  [[ "$active" == 1 ]] || fail "active secret versions were not reduced to placeholder v1: $active"
}

run_transport success
if [[ "$TRANSPORT_STATUS" -ne 0 ]]; then
  sed -n '1,80p' "$TEST_DIR/success.err" >&2
  sed -n '1,120p' "$MOCK_EVENT_LOG" >&2
  fail "valid exact-version transport failed"
fi
[[ "$(<"$MOCK_JOB_VERSION_FILE")" == 1 ]] || fail "successful transport did not restore job to v1"
assert_only_placeholder_active
[[ ! -e "$TEST_DIR/planted-helper-executed" ]] || fail "preclean executed a planted helper"
[[ -e "$MOCK_CONTAINER_SWEEPED" && ! -e "$MOCK_STALE_REMOTE_CONTAINER" ]] ||
  fail "preclean did not remove a bounded old-run container state file"
cmp -s "$MOCK_SECRET_CAPTURE" "$TEST_DIR/state-fixture.json" ||
  fail "Secret Manager did not receive the validated state"
[[ ! -e "$MOCK_REMOTE_STAGE" && ! -e "$MOCK_STALE_REMOTE_STAGE" &&
  ! -e "$MOCK_LEGACY_REMOTE_HELPER" && ! -e "$MOCK_LEGACY_REMOTE_STATE" &&
  ! -e "$MOCK_REMOTE_HELPER" && ! -e "$MOCK_REMOTE_STATE" && ! -e "$MOCK_REMOTE_CONTAINER" ]] ||
  fail "successful transport retained remote credential material"
[[ -z "$(find "$TEST_DIR/tmp" -mindepth 1 -maxdepth 1 -print -quit)" ]] ||
  fail "successful transport retained its local private directory"
credential_version="$(<"$MOCK_CREDENTIAL_VERSION_FILE")"
job_credential_line="$(event_line "job-update-${credential_version}")"
readiness_line="$(event_line readiness)"
job_restore_line="$(grep -n '^job-update-1$' "$MOCK_EVENT_LOG" | tail -n1 | cut -d: -f1)"
destroy_line="$(event_line "secret-destroy-${credential_version}")"
[[ "$job_credential_line" -lt "$readiness_line" && "$readiness_line" -lt "$job_restore_line" &&
  "$job_restore_line" -lt "$destroy_line" ]] ||
  fail "job binding, execution, restore, and exact destroy order drifted"
grep -Fq "secrets versions describe $credential_version" "$MOCK_GCLOUD_LOG" ||
  fail "known credential destruction was not attested by exact version"
if grep -Fq 'secrets versions destroy 1 ' "$MOCK_GCLOUD_LOG"; then
  fail "transport attempted to destroy placeholder version 1"
fi

run_transport first-run '' 1 false
[[ "$TRANSPORT_STATUS" -eq 0 ]] || fail "first transport from placeholder v1 failed"
assert_only_placeholder_active

run_transport version-list-omits-known version-list-omits-known
[[ "$TRANSPORT_STATUS" -eq 0 ]] || fail "eventually-consistent list omission caused a false failure"
credential_version="$(<"$MOCK_CREDENTIAL_VERSION_FILE")"
grep -Fq "secrets versions destroy $credential_version" "$MOCK_GCLOUD_LOG" ||
  fail "known credential relied on eventually-consistent list membership"
assert_only_placeholder_active

run_transport secret-destroy-ambiguous secret-destroy-ambiguous
[[ "$TRANSPORT_STATUS" -eq 0 ]] || fail "server-applied ambiguous destroy was not exactly attested"
assert_only_placeholder_active

run_transport secret-destroy-attest-lag secret-destroy-attest-lag
[[ "$TRANSPORT_STATUS" -eq 0 ]] || fail "bounded exact destroy attestation did not tolerate lag"
[[ "$(<"$MOCK_DESTROY_DESCRIBE_COUNT")" -ge 3 ]] || fail "destroy attestation did not retry"

for failure in browser-preflight version-list remote-stage-create remote-mint state-scp state-symlink state-mode state-invalid state-short-expiry job-update-credential job-update-credential-ambiguous job-attest-credential secret-add-malformed secret-attest readiness; do
  run_transport "$failure" "$failure"
  [[ "$TRANSPORT_STATUS" -ne 0 ]] || fail "$failure was accepted"
  if [[ "$failure" == browser-preflight ]]; then
    [[ "$(<"$MOCK_JOB_VERSION_FILE")" == 99 ]] ||
      fail "failed preflight mutated the prior job binding"
  else
    [[ "$(<"$MOCK_JOB_VERSION_FILE")" == 1 ]] || fail "$failure did not restore exact job version 1"
  fi
  if [[ -e "$MOCK_CREDENTIAL_VERSION_FILE" ]]; then
    credential_version="$(<"$MOCK_CREDENTIAL_VERSION_FILE")"
    [[ "$(<"$MOCK_VERSION_DIR/$credential_version")" == DESTROYED ]] ||
      fail "$failure retained the known/observed credential version"
  fi
done

run_transport secret-add-ambiguous secret-add-ambiguous
[[ "$TRANSPORT_STATUS" -eq 2 ]] || fail "ambiguous credential add was not kept failed"
credential_version="$(<"$MOCK_CREDENTIAL_VERSION_FILE")"
[[ "$(<"$MOCK_VERSION_DIR/$credential_version")" == DESTROYED ]] ||
  fail "observed ambiguous credential was not destroyed"

run_transport secret-add-ambiguous-identified secret-add-ambiguous-identified
[[ "$TRANSPORT_STATUS" -eq 2 ]] || fail "identified ambiguous credential add was false-green"
credential_version="$(<"$MOCK_CREDENTIAL_VERSION_FILE")"
grep -Fq "secrets versions destroy $credential_version" "$MOCK_GCLOUD_LOG" ||
  fail "identified ambiguous credential was not destroyed by exact version"

run_transport secret-add-ambiguous-unobserved secret-add-ambiguous-unobserved
[[ "$TRANSPORT_STATUS" -eq 2 ]] || fail "unobservable ambiguous add was false-green"
credential_version="$(<"$MOCK_CREDENTIAL_VERSION_FILE")"
[[ "$(<"$MOCK_VERSION_DIR/$credential_version")" == ENABLED ]] ||
  fail "unobservable ambiguity fixture did not exercise TTL fallback"
[[ "$(<"$MOCK_JOB_VERSION_FILE")" == 1 ]] || fail "ambiguous add did not leave the job inert"

run_transport secret-add-term secret-add-term
[[ "$TRANSPORT_STATUS" -eq 143 ]] || fail "TERM did not preserve the exact transport signal status"
credential_version="$(<"$MOCK_CREDENTIAL_VERSION_FILE")"
if [[ "$(<"$MOCK_VERSION_DIR/$credential_version")" != DESTROYED ]]; then
  sed -n '1,100p' "$TEST_DIR/secret-add-term.err" >&2
  sed -n '1,160p' "$MOCK_EVENT_LOG" >&2
  fail "TERM did not reconcile the server-created credential"
fi
[[ "$(<"$MOCK_JOB_VERSION_FILE")" == 1 ]] || fail "TERM did not restore exact job version 1"

run_transport secret-destroy-retry secret-destroy-once
[[ "$TRANSPORT_STATUS" -eq 2 ]] || fail "failed exact destroy was hidden"
credential_version="$(<"$MOCK_CREDENTIAL_VERSION_FILE")"
[[ "$(<"$MOCK_VERSION_DIR/$credential_version")" == DESTROYED ]] ||
  fail "EXIT cleanup did not retry the exact credential destroy"

run_transport secret-destroy-always secret-destroy-always
[[ "$TRANSPORT_STATUS" -eq 2 ]] || fail "persistent exact destroy failure was hidden"
grep -Fq 'cleanup failed: exact-secret-version' "$TEST_DIR/secret-destroy-always.err" ||
  fail "persistent exact destroy failure omitted categorical cleanup evidence"
[[ "$(<"$MOCK_JOB_VERSION_FILE")" == 1 ]] ||
  fail "persistent exact destroy failure left the job bound to the credential"

run_transport cleanup-job-restore cleanup-job-restore
[[ "$TRANSPORT_STATUS" -eq 2 ]] || fail "failed job restore was hidden"
grep -Fq 'cleanup failed: job-secret-restore' "$TEST_DIR/cleanup-job-restore.err" ||
  fail "combined failure omitted categorical job restore cleanup evidence"
credential_version="$(<"$MOCK_CREDENTIAL_VERSION_FILE")"
[[ "$(<"$MOCK_VERSION_DIR/$credential_version")" == DESTROYED ]] ||
  fail "job restore failure skipped exact credential destruction"

run_transport readiness-and-cleanup readiness-and-cleanup
[[ "$TRANSPORT_STATUS" -eq 2 ]] || fail "browser plus cleanup failure was hidden"
grep -Fq 'cleanup failed: job-secret-restore' "$TEST_DIR/readiness-and-cleanup.err" ||
  fail "browser failure hid its cleanup failure"

: >"$MOCK_GCLOUD_LOG"
set +e
GCLOUD_PROJECT="$TEST_PROJECT" \
SCRIBE_DEPLOYMENT_ENVIRONMENT=preview \
SCRIBE_REGION="$TEST_REGION" \
SCRIBE_ZONE="$TEST_ZONE" \
SCRIBE_INSTANCE=scribe \
GITHUB_RUN_ID="$TEST_RUN_ID" \
GITHUB_RUN_ATTEMPT="$TEST_RUN_ATTEMPT" \
RUNNER_TEMP="$TEST_DIR/tmp" \
  "$ROOT_DIR/ci/run-production-browser-readiness.sh" \
    "$TEST_JOB" "$TEST_SECRET" "$TEST_DIR/artifacts/invalid-environment.log" \
    >"$TEST_DIR/invalid-environment.out" 2>"$TEST_DIR/invalid-environment.err"
invalid_status=$?
set -e
[[ "$invalid_status" -eq 2 && ! -s "$MOCK_GCLOUD_LOG" ]] ||
  fail "invalid production identity reached a cloud command"

secret_pattern='AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA|scribe_session|storage-state\.json|scribe-production-browser-state-[0-9]'
if rg -n "$secret_pattern" \
  "$TEST_DIR"/*.out "$TEST_DIR"/*.err "$TEST_DIR"/artifacts >/dev/null; then
  fail "credential content or path escaped to console or diagnostics"
fi
if rg -n 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA|scribe_session' \
  "$MOCK_ALL_GCLOUD_LOG" "$MOCK_EVENT_LOG" >/dev/null; then
  fail "credential content escaped into command or event logs"
fi

echo "Production browser readiness exact-version transport, isolation, and cleanup contracts passed."
