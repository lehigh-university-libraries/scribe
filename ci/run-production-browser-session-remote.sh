#!/usr/bin/env bash

set -euo pipefail

readonly PROJECT_DIR="/mnt/disks/data/scribe/prod"
readonly COMPOSE_OVERLAY="/home/cloud-compose/scribe-runtime.compose.yaml"
readonly COMPOSE_PROJECT="scribe-prod"
readonly MIN_STATE_BYTES=128
readonly MAX_STATE_BYTES=8192

fail() {
  printf 'Production browser session remote helper failed: %s\n' "$1" >&2
  exit 2
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required-command"
}

[[ "$#" -eq 4 ]] || fail "arguments"

mode="$1"
run_id="$2"
run_attempt="$3"
stage_dir="$4"

[[ "$mode" == "mint" || "$mode" == "cleanup" ]] || fail "mode"
[[ "$run_id" =~ ^[1-9][0-9]{0,19}$ ]] || fail "run-identity"
[[ "$run_attempt" =~ ^[1-9][0-9]{0,4}$ ]] || fail "run-identity"

for command in chmod docker id readlink rm stat; do
  require_command "$command"
done

project_real_path="$(readlink -f -- "$PROJECT_DIR" 2>/dev/null)" || fail "project-boundary"
[[ "$project_real_path" == "$PROJECT_DIR" ]] || fail "project-boundary"
[[ "$(stat -c '%F' -- "$PROJECT_DIR" 2>/dev/null)" == "directory" ]] || fail "project-boundary"

readonly container_state="/tmp/scribe-browser-session-${run_id}-${run_attempt}.json"
[[ "$stage_dir" =~ ^/tmp/scribe-production-browser-${run_id}-${run_attempt}\.[A-Za-z0-9]{10}$ ]] ||
  fail "stage-boundary"
stage_real_path="$(readlink -f -- "$stage_dir" 2>/dev/null)" || fail "stage-boundary"
[[ "$stage_real_path" == "$stage_dir" ]] || fail "stage-boundary"
[[ -d "$stage_dir" && ! -L "$stage_dir" ]] || fail "stage-boundary"
[[ "$(stat -c '%F' -- "$stage_dir" 2>/dev/null)" == "directory" ]] || fail "stage-boundary"
[[ "$(stat -c '%u' -- "$stage_dir" 2>/dev/null)" == "$(id -u)" ]] || fail "stage-owner"
[[ "$(stat -c '%a' -- "$stage_dir" 2>/dev/null)" == "700" ]] || fail "stage-mode"
readonly remote_state="${stage_dir}/storage-state.json"
readonly -a compose=(
  docker compose
  --project-directory "$PROJECT_DIR"
  --project-name "$COMPOSE_PROJECT"
  -f "$PROJECT_DIR/docker-compose.yaml"
  -f "$COMPOSE_OVERLAY"
)

preserve_remote_state=false

remove_container_state() {
  "${compose[@]}" exec -T api rm -f -- "$container_state" >/dev/null 2>&1
}

cleanup_material() {
  local status=0
  remove_container_state || status=1
  if [[ "$preserve_remote_state" != true ]]; then
    rm -f -- "$remote_state" >/dev/null 2>&1 || status=1
  fi
  return "$status"
}

cleanup_on_exit() {
  local status="$?"
  trap - EXIT INT TERM
  set +e
  cleanup_material
  exit "$status"
}

trap cleanup_on_exit EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if [[ "$mode" == "cleanup" ]]; then
  preserve_remote_state=false
  cleanup_material || fail "cleanup"
  exit 0
fi

umask 077
preserve_remote_state=false
rm -f -- "$remote_state" >/dev/null 2>&1 || fail "host-cleanup"
remove_container_state || fail "container-cleanup"

"${compose[@]}" exec -T api \
  /app/scribe-browser-session --output "$container_state" >/dev/null 2>&1 ||
  fail "mint"

"${compose[@]}" cp "api:${container_state}" "$remote_state" >/dev/null 2>&1 ||
  fail "container-copy"

# The container credential is discarded before any host-side validation or
# transfer. The exit trap repeats this deletion on every failure path.
remove_container_state || fail "container-cleanup"

[[ -f "$remote_state" && ! -L "$remote_state" ]] || fail "state-type"
chmod 0600 -- "$remote_state" >/dev/null 2>&1 || fail "state-mode"
[[ "$(stat -c '%F' -- "$remote_state" 2>/dev/null)" == "regular file" ]] || fail "state-type"
[[ "$(stat -c '%a' -- "$remote_state" 2>/dev/null)" == "600" ]] || fail "state-mode"
state_size="$(stat -c '%s' -- "$remote_state" 2>/dev/null)" || fail "state-size"
[[ "$state_size" =~ ^[0-9]+$ ]] || fail "state-size"
((state_size >= MIN_STATE_BYTES && state_size <= MAX_STATE_BYTES)) || fail "state-size"

preserve_remote_state=true
