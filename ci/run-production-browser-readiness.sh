#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly ROOT_DIR
readonly REMOTE_HELPER_SOURCE="$ROOT_DIR/ci/run-production-browser-session-remote.sh"
readonly PROJECT_DIR="/mnt/disks/data/scribe/prod"
readonly COMPOSE_OVERLAY="/home/cloud-compose/scribe-runtime.compose.yaml"
readonly COMPOSE_PROJECT="scribe-prod"
readonly SSH_KEY_TTL="50m"
readonly MIN_STATE_BYTES=128
readonly MAX_STATE_BYTES=8192
readonly MAX_VERSION_INVENTORY_BYTES=32768
readonly MAX_VERSION_INVENTORY_ENTRIES=64
readonly SECRET_CONTROL_PLANE_TIMEOUT=30s
readonly SECRET_CONTROL_PLANE_KILL_AFTER=5s
readonly JOB_UPDATE_TIMEOUT=180s
readonly JOB_UPDATE_KILL_AFTER=5s
readonly JOB_ATTEST_TIMEOUT=30s
readonly JOB_ATTEST_KILL_AFTER=5s
readonly JOB_ATTEST_ATTEMPTS=3
readonly JOB_ATTEST_POLL_SECONDS=2
readonly REMOTE_CALL_TIMEOUT=180s
readonly REMOTE_CALL_KILL_AFTER=5s
readonly SECRET_DESTROY_ATTEST_ATTEMPTS=5
readonly SECRET_DESTROY_POLL_SECONDS=2
readonly BROWSER_STORAGE_STATE_ENV="SCRIBE_BROWSER_STORAGE_STATE_JSON"
# shellcheck disable=SC2016 # Literal fixed script evaluated only inside the API container.
readonly CONTAINER_SWEEP_SCRIPT='for candidate in /tmp/scribe-browser-session-*.json; do if test ! -e "$candidate" && test ! -L "$candidate"; then continue; fi; stem=${candidate#/tmp/scribe-browser-session-}; run=${stem%-*}; attempt=${stem##*-}; attempt=${attempt%.json}; case "$run" in ""|0*|*[!0-9]*) continue ;; esac; case "$attempt" in ""|0*|*[!0-9]*) continue ;; esac; test "${#run}" -le 20 || continue; test "${#attempt}" -le 5 || continue; rm -f -- "$candidate" || exit 1; done'

fail() {
  printf 'Production browser readiness transport failed: %s\n' "$1" >&2
  exit 2
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required-command"
}

usage() {
  printf '%s\n' 'Usage: run-production-browser-readiness.sh JOB SECRET DIAGNOSTICS_FILE' >&2
}

[[ "$#" -eq 3 ]] || {
  usage
  fail "arguments"
}

job="$1"
secret="$2"
diagnostics_file="$3"
project="${GCLOUD_PROJECT:-}"
deployment_environment="${SCRIBE_DEPLOYMENT_ENVIRONMENT:-}"
region="${SCRIBE_REGION:-}"
zone="${SCRIBE_ZONE:-}"
instance="${SCRIBE_INSTANCE:-}"
run_id="${GITHUB_RUN_ID:-}"
run_attempt="${GITHUB_RUN_ATTEMPT:-}"

[[ "$project" =~ ^[a-z][a-z0-9-]{4,28}[a-z0-9]$ ]] || fail "project"
[[ "$deployment_environment" == "production" ]] || fail "environment"
[[ "$region" =~ ^[a-z]+-[a-z]+[0-9]+$ ]] || fail "region"
[[ "$zone" =~ ^${region}-[a-z]$ ]] || fail "zone"
[[ "$instance" == "scribe" ]] || fail "instance"
[[ "$run_id" =~ ^[1-9][0-9]{0,19}$ ]] || fail "run-identity"
[[ "$run_attempt" =~ ^[1-9][0-9]{0,4}$ ]] || fail "run-identity"
[[ "$job" =~ ^scribe-browser-([0-9a-f]{8})$ ]] || fail "job"
job_suffix="${BASH_REMATCH[1]}"
[[ "$secret" =~ ^scribe-browser-session-([0-9a-f]{8})$ ]] || fail "secret"
[[ "${BASH_REMATCH[1]}" == "$job_suffix" ]] || fail "resource-pair"
[[ "$diagnostics_file" == *.log && "$diagnostics_file" != *$'\n'* ]] || fail "diagnostics"
diagnostics_dir="$(dirname -- "$diagnostics_file")"
[[ -d "$diagnostics_dir" && ! -L "$diagnostics_dir" ]] || fail "diagnostics"
[[ ! -L "$diagnostics_file" ]] || fail "diagnostics"
[[ -f "$REMOTE_HELPER_SOURCE" && ! -L "$REMOTE_HELPER_SOURCE" ]] || fail "remote-helper"

for command in chmod gcloud install jq mktemp rm sha256sum sleep ssh-keygen stat timeout; do
  require_command "$command"
done

umask 077
temp_root="${RUNNER_TEMP:-${TMPDIR:-/tmp}}"
[[ -d "$temp_root" && ! -L "$temp_root" ]] || fail "temporary-root"
temp_dir="$(mktemp -d "$temp_root/scribe-production-browser.XXXXXX")" || fail "temporary-directory"
[[ -d "$temp_dir" && ! -L "$temp_dir" ]] || fail "temporary-directory"
[[ "$(stat -c '%a' -- "$temp_dir" 2>/dev/null)" == "700" ]] || fail "temporary-directory-mode"

ssh_key="$temp_dir/id_ed25519"
known_hosts="$temp_dir/known_hosts"
staged_helper="$temp_dir/remote-helper.sh"
local_state="$temp_dir/storage-state.json"
version_inventory="$temp_dir/secret-versions.tsv"
job_update_json="$temp_dir/job-update.json"
job_describe_json="$temp_dir/job-describe.json"
secret_version_json="$temp_dir/secret-version.json"
legacy_remote_helper="/tmp/scribe-production-browser-helper-${run_id}-${run_attempt}.sh"
legacy_remote_state="/tmp/scribe-production-browser-state-${run_id}-${run_attempt}.json"
remote_stage_dir=""
remote_helper=""
remote_state=""
container_state="/tmp/scribe-browser-session-${run_id}-${run_attempt}.json"
credential_version=""
cleanup_version=""
secret_add_possible=false
job_restore_required=false
remote_material_possible=false

readonly legacy_remote_helper legacy_remote_state container_state

ssh_common=(
  "cloud-compose@${instance}"
  --project="$project"
  --zone="$zone"
  --tunnel-through-iap
  --ssh-key-file="$ssh_key"
  --ssh-key-expire-after="$SSH_KEY_TTL"
  --ssh-flag="-o UserKnownHostsFile=${known_hosts}"
  --ssh-flag="-o GlobalKnownHostsFile=/dev/null"
  --ssh-flag="-o StrictHostKeyChecking=accept-new"
  --ssh-flag="-o IdentitiesOnly=yes"
  --ssh-flag="-o ConnectTimeout=30"
  --ssh-flag="-o ServerAliveInterval=15"
  --ssh-flag="-o ServerAliveCountMax=4"
  --quiet
)

scp_common=(
  --project="$project"
  --zone="$zone"
  --tunnel-through-iap
  --ssh-key-file="$ssh_key"
  --ssh-key-expire-after="$SSH_KEY_TTL"
  --scp-flag="-o UserKnownHostsFile=${known_hosts}"
  --scp-flag="-o GlobalKnownHostsFile=/dev/null"
  --scp-flag="-o StrictHostKeyChecking=accept-new"
  --scp-flag="-o IdentitiesOnly=yes"
  --scp-flag="-o ConnectTimeout=30"
  --scp-flag="-o ServerAliveInterval=15"
  --scp-flag="-o ServerAliveCountMax=4"
  --quiet
)

remote_cleanup_command() {
  local command stale_stage_find

  command="set +e; status=0"
  if [[ -n "$remote_stage_dir" ]]; then
    command+="; docker compose --project-directory '$PROJECT_DIR' --project-name '$COMPOSE_PROJECT' -f '$PROJECT_DIR/docker-compose.yaml' -f '$COMPOSE_OVERLAY' exec -T api rm -f -- '$container_state' >/dev/null 2>&1 || status=1; rm -f -- '$legacy_remote_state' '$legacy_remote_helper' '$remote_state' '$remote_helper' >/dev/null 2>&1 || status=1; rmdir -- '$remote_stage_dir' >/dev/null 2>&1 || status=1"
  else
    stale_stage_find="find /tmp -xdev -mindepth 1 -maxdepth 1 -regextype posix-extended -regex '/tmp/scribe-production-browser-[1-9][0-9]{0,19}-[1-9][0-9]{0,4}\.[A-Za-z0-9]{10}'"
    command+="; docker compose --project-directory '$PROJECT_DIR' --project-name '$COMPOSE_PROJECT' -f '$PROJECT_DIR/docker-compose.yaml' -f '$COMPOSE_OVERLAY' exec -T api sh -ceu '$CONTAINER_SWEEP_SCRIPT' >/dev/null 2>&1 || status=1; rm -f -- '$legacy_remote_state' '$legacy_remote_helper' >/dev/null 2>&1 || status=1; $stale_stage_find -type d -user cloud-compose -perm 0700 -exec rm -f -- '{}/storage-state.json' '{}/helper.sh' \; -exec rmdir -- '{}' \; >/dev/null 2>&1 || status=1; stale_stage=\$($stale_stage_find -print -quit 2>/dev/null) || status=1; test -z \"\$stale_stage\" || status=1"
  fi
  # shellcheck disable=SC2016 # $status belongs to the fixed remote command.
  printf '%s; exit $status' "$command"
}

cleanup_remote() {
  local command

  [[ "$remote_material_possible" == true ]] || return 0
  command="$(remote_cleanup_command)"
  timeout --signal=TERM --kill-after="$REMOTE_CALL_KILL_AFTER" "$REMOTE_CALL_TIMEOUT" \
    gcloud compute ssh "${ssh_common[@]}" \
      --command="$command" >/dev/null 2>&1
}

attest_job_secret_version_file() {
  local source_file="$1" expected_version="$2"

  jq -e \
    --arg job "$job" \
    --arg secret "$secret" \
    --arg version "$expected_version" \
    --arg env_name "$BROWSER_STORAGE_STATE_ENV" '
      def leaf:
        if type == "string" then split("/") | last else "" end;
      def secret_match($candidate):
        ($candidate | type) == "string"
        and (($candidate == $secret) or ($candidate | endswith("/secrets/" + $secret)));
      def ref:
        if (.valueFrom.secretKeyRef? | type) == "object" then
          {secret: .valueFrom.secretKeyRef.name, version: .valueFrom.secretKeyRef.key}
        elif (.valueSource.secretKeyRef? | type) == "object" then
          {secret: .valueSource.secretKeyRef.secret, version: .valueSource.secretKeyRef.version}
        else {}
        end;
      (((.metadata.name // .name // "") | leaf) == $job) as $job_matches
      | ([
        (.spec.template.spec.template.spec.containers // [])[]?,
        (.spec.template.spec.template.containers // [])[]?,
        (.template.template.containers // [])[]?,
        (.template.containers // [])[]?
      ] | [.[].env[]? | select(.name == $env_name)]) as $matches
      | $job_matches
      and (($matches | length) == 1)
      and (($matches[0] | ref) as $reference
        | secret_match($reference.secret)
        and (($reference.version | tostring) == $version)
        and (($matches[0].value // "") == ""))
    ' "$source_file" >/dev/null 2>&1
}

set_job_secret_version() {
  local expected_version="$1" attempt

  [[ "$expected_version" =~ ^[1-9][0-9]{0,19}$ ]] || return 1
  install -m 0600 /dev/null "$job_update_json" >/dev/null 2>&1 || return 1
  if ! timeout --signal=TERM --kill-after="$JOB_UPDATE_KILL_AFTER" "$JOB_UPDATE_TIMEOUT" \
    gcloud run jobs update "$job" \
      --project "$project" \
      --region "$region" \
      --update-secrets="${BROWSER_STORAGE_STATE_ENV}=${secret}:${expected_version}" \
      --format=json \
      --quiet >"$job_update_json" 2>/dev/null; then
    return 1
  fi
  attest_job_secret_version_file "$job_update_json" "$expected_version" || return 1

  for ((attempt = 1; attempt <= JOB_ATTEST_ATTEMPTS; attempt++)); do
    install -m 0600 /dev/null "$job_describe_json" >/dev/null 2>&1 || return 1
    if timeout --signal=TERM --kill-after="$JOB_ATTEST_KILL_AFTER" "$JOB_ATTEST_TIMEOUT" \
      gcloud run jobs describe "$job" \
        --project "$project" \
        --region "$region" \
        --format=json >"$job_describe_json" 2>/dev/null \
      && attest_job_secret_version_file "$job_describe_json" "$expected_version"; then
      return 0
    fi
    if ((attempt < JOB_ATTEST_ATTEMPTS)); then
      sleep "$JOB_ATTEST_POLL_SECONDS"
    fi
  done
  return 1
}

secret_version_state() {
  local version="$1"

  install -m 0600 /dev/null "$secret_version_json" >/dev/null 2>&1 || return 1
  timeout --signal=TERM --kill-after="$SECRET_CONTROL_PLANE_KILL_AFTER" "$SECRET_CONTROL_PLANE_TIMEOUT" \
    gcloud secrets versions describe "$version" \
      --secret="$secret" \
      --project="$project" \
      --format=json >"$secret_version_json" 2>/dev/null || return 1
  jq -er --arg secret "$secret" --arg version "$version" '
    (.name // "") as $name
    | select(($name | type) == "string" and ($name | length) <= 512)
    | ($name | split("/")) as $parts
    | select(($parts | length) >= 6)
    | select($parts[-1] == $version and $parts[-2] == "versions")
    | select($parts[-3] == $secret and $parts[-4] == "secrets")
    | (.state // "")
    | select(. == "ENABLED" or . == "DISABLED" or . == "DESTROYED")
  ' "$secret_version_json" 2>/dev/null
}

destroy_and_prove_secret_version() {
  local version="$1" attempt state=""

  [[ "$version" =~ ^([2-9]|[1-9][0-9]{1,19})$ ]] || return 1
  state="$(secret_version_state "$version")" || state=""
  [[ "$state" != "DESTROYED" ]] || return 0

  if ! timeout --signal=TERM --kill-after="$SECRET_CONTROL_PLANE_KILL_AFTER" "$SECRET_CONTROL_PLANE_TIMEOUT" \
    gcloud secrets versions destroy "$version" \
      --secret="$secret" \
      --project="$project" \
      --quiet >/dev/null 2>&1; then
    state="$(secret_version_state "$version")" || state=""
    [[ "$state" == "DESTROYED" ]] || return 1
    return 0
  fi

  for ((attempt = 1; attempt <= SECRET_DESTROY_ATTEST_ATTEMPTS; attempt++)); do
    state="$(secret_version_state "$version")" || state=""
    [[ "$state" != "DESTROYED" ]] || return 0
    if ((attempt < SECRET_DESTROY_ATTEST_ATTEMPTS)); then
      sleep "$SECRET_DESTROY_POLL_SECONDS"
    fi
  done
  return 1
}

load_active_version_inventory() {
  local inventory_size

  install -m 0600 /dev/null "$version_inventory" >/dev/null 2>&1 || return 1
  timeout --signal=TERM --kill-after="$SECRET_CONTROL_PLANE_KILL_AFTER" "$SECRET_CONTROL_PLANE_TIMEOUT" \
    gcloud secrets versions list "$secret" \
      --project="$project" \
      --filter='state!=DESTROYED' \
      --format='value(name.basename(),state)' \
      >"$version_inventory" 2>/dev/null || return 1
  [[ -f "$version_inventory" && ! -L "$version_inventory" ]] || return 1
  [[ "$(stat -c '%F' -- "$version_inventory" 2>/dev/null)" == "regular file" ]] || return 1
  [[ "$(stat -c '%a' -- "$version_inventory" 2>/dev/null)" == "600" ]] || return 1
  inventory_size="$(stat -c '%s' -- "$version_inventory" 2>/dev/null)" || return 1
  [[ "$inventory_size" =~ ^[0-9]+$ ]] || return 1
  ((inventory_size > 0 && inventory_size <= MAX_VERSION_INVENTORY_BYTES)) || return 1
}

cleanup_observed_secret_versions() {
  local entry_count=0 extra placeholder_seen=false state version
  local -a versions=()
  declare -A seen_versions=()

  load_active_version_inventory || return 1
  while IFS=$'\t' read -r version state extra; do
    entry_count=$((entry_count + 1))
    ((entry_count <= MAX_VERSION_INVENTORY_ENTRIES)) || return 1
    [[ -z "$extra" && "$version" =~ ^[1-9][0-9]{0,19}$ ]] || return 1
    [[ "$state" == "ENABLED" || "$state" == "DISABLED" ]] || return 1
    [[ -z "${seen_versions[$version]:-}" ]] || return 1
    seen_versions[$version]=true
    [[ "$version" != 1 ]] || placeholder_seen=true
    [[ "$version" == 1 ]] || versions+=("$version")
  done <"$version_inventory"
  ((entry_count > 0)) || return 1
  [[ "$placeholder_seen" == true ]] || return 1

  for version in "${versions[@]}"; do
    destroy_and_prove_secret_version "$version" || return 1
  done
}

cleanup_on_exit() {
  local status="$?" cleanup_failed=false

  trap - EXIT INT TERM
  set +e
  rm -f -- "$local_state" >/dev/null 2>&1 || {
    printf 'Production browser readiness cleanup failed: local-state.\n' >&2
    cleanup_failed=true
  }
  if ! cleanup_remote; then
    printf 'Production browser readiness cleanup failed: remote-state.\n' >&2
    cleanup_failed=true
  fi
  if [[ "$job_restore_required" == true ]] && ! set_job_secret_version 1; then
    printf 'Production browser readiness cleanup failed: job-secret-restore.\n' >&2
    cleanup_failed=true
  fi
  if [[ -n "$cleanup_version" ]] && ! destroy_and_prove_secret_version "$cleanup_version"; then
    printf 'Production browser readiness cleanup failed: exact-secret-version.\n' >&2
    cleanup_failed=true
  fi
  if [[ "$secret_add_possible" == true ]] && ! cleanup_observed_secret_versions; then
    printf 'Production browser readiness cleanup failed: observed-secret-versions.\n' >&2
    cleanup_failed=true
  fi
  rm -rf -- "$temp_dir" >/dev/null 2>&1 || {
    printf 'Production browser readiness cleanup failed: temporary-state.\n' >&2
    cleanup_failed=true
  }
  if [[ "$cleanup_failed" == true && "$status" -eq 0 ]]; then
    status=2
  fi
  exit "$status"
}

trap cleanup_on_exit EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

install -m 0600 /dev/null "$known_hosts" >/dev/null 2>&1 || fail "known-hosts"
read -r expected_remote_helper_sha _ < <(sha256sum -- "$REMOTE_HELPER_SOURCE")
[[ "$expected_remote_helper_sha" =~ ^[0-9a-f]{64}$ ]] || fail "remote-helper-digest"
install -m 0700 "$REMOTE_HELPER_SOURCE" "$staged_helper" >/dev/null 2>&1 || fail "remote-helper"
read -r staged_remote_helper_sha _ < <(sha256sum -- "$staged_helper")
[[ "$staged_remote_helper_sha" == "$expected_remote_helper_sha" ]] || fail "remote-helper-digest"

"$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  --preflight-only "$job" browser || fail "browser-preflight"

# Every protected run repairs an interrupted predecessor before minting a new
# session. Version 1 is the Terraform-owned inert placeholder and is never
# destroyed by this transport.
job_restore_required=true
set_job_secret_version 1 || fail "job-secret-restore"
cleanup_observed_secret_versions || fail "stale-secret-cleanup"

ssh-keygen -q -t ed25519 -N '' \
  -C "scribe-production-browser-${run_id}-${run_attempt}" \
  -f "$ssh_key" >/dev/null 2>&1 || fail "ssh-key"
chmod 0600 -- "$ssh_key" "$ssh_key.pub" >/dev/null 2>&1 || fail "ssh-key-mode"

remote_material_possible=true
cleanup_remote >/dev/null 2>&1 || fail "remote-preclean"

remote_stage_command="set -eu; umask 077; stage=\$(mktemp -d -- '/tmp/scribe-production-browser-${run_id}-${run_attempt}.XXXXXXXXXX'); if ! test -d \"\$stage\" || test -L \"\$stage\" || ! test \"\$(stat -c '%F' -- \"\$stage\")\" = 'directory' || ! test \"\$(stat -c '%U' -- \"\$stage\")\" = 'cloud-compose' || ! test \"\$(stat -c '%a' -- \"\$stage\")\" = '700'; then rmdir -- \"\$stage\" >/dev/null 2>&1 || true; exit 1; fi; printf '%s\\n' \"\$stage\""
remote_stage_candidate="$(
  timeout --signal=TERM --kill-after="$REMOTE_CALL_KILL_AFTER" "$REMOTE_CALL_TIMEOUT" \
    gcloud compute ssh "${ssh_common[@]}" \
      --command="$remote_stage_command" 2>/dev/null
)" || fail "remote-stage-create"
[[ "$remote_stage_candidate" =~ ^/tmp/scribe-production-browser-${run_id}-${run_attempt}\.[A-Za-z0-9]{10}$ ]] ||
  fail "remote-stage-identity"
remote_stage_dir="$remote_stage_candidate"
remote_helper="${remote_stage_dir}/helper.sh"
remote_state="${remote_stage_dir}/storage-state.json"
readonly remote_stage_dir remote_helper remote_state

timeout --signal=TERM --kill-after="$REMOTE_CALL_KILL_AFTER" "$REMOTE_CALL_TIMEOUT" \
  gcloud compute scp "$staged_helper" "cloud-compose@${instance}:${remote_helper}" \
    "${scp_common[@]}" >/dev/null 2>&1 || fail "remote-helper-copy"

mint_command="set -eu; test -d '$remote_stage_dir' && ! test -L '$remote_stage_dir' && test \"\$(stat -c '%F' -- '$remote_stage_dir')\" = 'directory' && test \"\$(stat -c '%U' -- '$remote_stage_dir')\" = 'cloud-compose' && test \"\$(stat -c '%a' -- '$remote_stage_dir')\" = '700' && test -f '$remote_helper' && ! test -L '$remote_helper' && test \"\$(stat -c '%F' -- '$remote_helper')\" = 'regular file' && test \"\$(stat -c '%U' -- '$remote_helper')\" = 'cloud-compose' && chmod 0700 -- '$remote_helper' && test \"\$(stat -c '%a' -- '$remote_helper')\" = '700' && helper_digest=\$(sha256sum -- '$remote_helper') && helper_digest=\${helper_digest%% *} && test \"\$helper_digest\" = '$expected_remote_helper_sha' && exec bash '$remote_helper' mint '$run_id' '$run_attempt' '$remote_stage_dir'"
timeout --signal=TERM --kill-after="$REMOTE_CALL_KILL_AFTER" "$REMOTE_CALL_TIMEOUT" \
  gcloud compute ssh "${ssh_common[@]}" \
    --command="$mint_command" >/dev/null 2>&1 || fail "remote-mint"

timeout --signal=TERM --kill-after="$REMOTE_CALL_KILL_AFTER" "$REMOTE_CALL_TIMEOUT" \
  gcloud compute scp "cloud-compose@${instance}:${remote_state}" "$local_state" \
    "${scp_common[@]}" >/dev/null 2>&1 || fail "state-copy"

cleanup_remote >/dev/null 2>&1 || fail "remote-cleanup"
remote_material_possible=false

[[ -f "$local_state" && ! -L "$local_state" ]] || fail "state-type"
[[ "$(stat -c '%F' -- "$local_state" 2>/dev/null)" == "regular file" ]] || fail "state-type"
[[ "$(stat -c '%a' -- "$local_state" 2>/dev/null)" == "600" ]] || fail "state-mode"
state_size="$(stat -c '%s' -- "$local_state" 2>/dev/null)" || fail "state-size"
[[ "$state_size" =~ ^[0-9]+$ ]] || fail "state-size"
((state_size >= MIN_STATE_BYTES && state_size <= MAX_STATE_BYTES)) || fail "state-size"

jq -e '
  type == "object" and
  (keys | sort) == ["cookies", "origins"] and
  (.origins | type == "array" and length == 0) and
  (.cookies | type == "array" and length == 1) and
  (.cookies[0] | type == "object") and
  (.cookies[0] | keys | sort) ==
    ["domain", "expires", "httpOnly", "name", "path", "sameSite", "secure", "value"] and
  (.cookies[0].name == "scribe_session") and
  (.cookies[0].value | type == "string" and test("^[A-Za-z0-9_-]{64}$")) and
  (.cookies[0].domain | type == "string" and test("^\\.?[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$")) and
  (.cookies[0].path == "/") and
  (.cookies[0].expires | type == "number" and . >= (now + 2460) and . <= (now + 3000)) and
  (.cookies[0].httpOnly == true) and
  (.cookies[0].secure == true) and
  (.cookies[0].sameSite == "Lax")
' "$local_state" >/dev/null 2>&1 || fail "state-contract"

read -r state_sha _ < <(sha256sum -- "$local_state")
[[ "$state_sha" =~ ^[0-9a-f]{64}$ ]] || fail "state-digest"

secret_add_possible=true
set +e
secret_version_candidate="$(
  timeout --signal=TERM --kill-after="$SECRET_CONTROL_PLANE_KILL_AFTER" "$SECRET_CONTROL_PLANE_TIMEOUT" \
    gcloud secrets versions add "$secret" \
      --project="$project" \
      --data-file="$local_state" \
      --format='value(name.basename())' 2>/dev/null
)"
secret_add_status=$?
set -e
if [[ "$secret_version_candidate" =~ ^([2-9]|[1-9][0-9]{1,19})$ ]]; then
  cleanup_version="$secret_version_candidate"
fi
((secret_add_status == 0)) || fail "secret-version-create"
[[ -n "$cleanup_version" ]] || fail "secret-version-identity"
credential_version="$cleanup_version"
[[ "$(secret_version_state "$credential_version")" == "ENABLED" ]] || fail "secret-version-attestation"

set_job_secret_version "$credential_version" || fail "job-secret-credential"
jq -e '.cookies[0].expires >= (now + 2460)' "$local_state" >/dev/null 2>&1 ||
  fail "state-expiry-after-binding"
rm -f -- "$local_state" >/dev/null 2>&1 || fail "local-state-cleanup"
[[ ! -e "$local_state" && ! -L "$local_state" ]] || fail "local-state-cleanup"

SCRIBE_BROWSER_EXPECTED_SECRET_VERSION="$credential_version" \
SCRIBE_BROWSER_EXPECTED_STORAGE_STATE_SHA256="$state_sha" \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
    "$job" browser "$diagnostics_file" || fail "browser-execution"

set_job_secret_version 1 || fail "job-secret-restore"
destroy_and_prove_secret_version "$credential_version" || fail "exact-secret-version-cleanup"
cleanup_version=""
cleanup_observed_secret_versions || fail "observed-secret-cleanup"
credential_version=""
secret_add_possible=false
job_restore_required=false

printf 'Production browser readiness passed for %s.\n' "$job"
