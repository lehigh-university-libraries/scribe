#!/usr/bin/env bash

set -euo pipefail

fail() {
  printf '%s\n' "$1" >&2
  exit 1
}

: "${GCLOUD_PROJECT:?GCLOUD_PROJECT is required}"
: "${TF_STATE_BUCKET:?TF_STATE_BUCKET is required}"
: "${TF_WORKSPACE:?TF_WORKSPACE is required}"

[[ "$GCLOUD_PROJECT" =~ ^[a-z][a-z0-9-]{4,28}[a-z0-9]$ ]] ||
  fail "GCLOUD_PROJECT must be a valid Google Cloud project ID."
[[ "$TF_STATE_BUCKET" =~ ^[a-z0-9][a-z0-9._-]{1,61}[a-z0-9]$ ]] ||
  fail "TF_STATE_BUCKET must be a valid Google Cloud Storage bucket name."
[[ "$TF_WORKSPACE" =~ ^pr-[1-9][0-9]*$ ]] ||
  fail "TF_WORKSPACE must identify one preview workspace as pr-N."

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
resolver="$script_dir/resolve-destroy-inputs.sh"
[ -x "$resolver" ] || fail "Destroy input resolver is unavailable."

for command in terraform gcloud jq; do
  command -v "$command" >/dev/null 2>&1 || fail "Required recovery command is unavailable: $command"
done

umask 077
recovery_tmp="$(mktemp -d)"
trap 'rm -rf -- "$recovery_tmp"' EXIT

current_state="$recovery_tmp/current.tfstate"
if ! terraform state pull >"$current_state"; then
  fail "Current Terraform state could not be read."
fi

# Recovery is only valid for the narrow partial-destroy failure where the live
# state remains readable but its immutable deployment_inputs output is gone.
if ! jq -e '
  type == "object" and
  (.serial | type == "number" and floor == . and . >= 0) and
  (.lineage | type == "string" and length > 0) and
  (.outputs | type == "object") and
  (.outputs | has("deployment_inputs") | not)
' "$current_state" >/dev/null 2>&1; then
  fail "Current Terraform state is invalid or already contains deployment_inputs."
fi

current_serial="$(jq -r '.serial' "$current_state")"
current_lineage="$(jq -r '.lineage' "$current_state")"
state_uri="gs://${TF_STATE_BUCKET}/scribe/${TF_WORKSPACE}.tfstate"

if ! version_listing="$(gcloud storage ls --all-versions "$state_uri")"; then
  fail "Terraform state object versions could not be listed."
fi
[ -n "$version_listing" ] || fail "No historical Terraform state versions were found."

declare -A seen_generations=()
declare -A payload_by_serial=()
best_serial=-1
best_payload=""
candidate_state="$recovery_tmp/candidate.tfstate"

while IFS= read -r object_version; do
  version_prefix="${state_uri}#"
  [[ "$object_version" == "$version_prefix"* ]] ||
    fail "Terraform state version listing was malformed."
  generation="${object_version#"$version_prefix"}"
  [[ "$generation" =~ ^[1-9][0-9]*$ ]] ||
    fail "Terraform state version listing was malformed."
  if [[ -n "${seen_generations[$generation]+present}" ]]; then
    fail "Terraform state version listing contained a duplicate generation."
  fi
  seen_generations[$generation]=1

  if ! gcloud storage cat "${state_uri}#${generation}" >"$candidate_state"; then
    fail "A listed Terraform state version could not be read."
  fi

  # Ignore snapshots that are malformed, belong to another lineage, are not
  # older than the live state, or do not contain a candidate output. Their
  # contents are never included in diagnostics.
  if ! candidate="$(jq -ce \
    --arg lineage "$current_lineage" \
    --argjson current_serial "$current_serial" '
      select(
        type == "object" and
        (.lineage == $lineage) and
        (.lineage | type == "string" and length > 0) and
        (.serial | type == "number" and floor == . and . >= 0 and . < $current_serial) and
        (.outputs | type == "object") and
        (.outputs.deployment_inputs | type == "object") and
        (.outputs.deployment_inputs | has("value"))
      )
      | {serial, deployment_inputs: .outputs.deployment_inputs.value}
    ' "$candidate_state" 2>/dev/null)"; then
    continue
  fi

  candidate_serial="$(jq -r '.serial' <<<"$candidate")"
  candidate_inputs="$(jq -c '.deployment_inputs' <<<"$candidate")"
  if ! normalized_inputs="$(
    printf '%s\n' "$candidate_inputs" |
      GCLOUD_PROJECT="$GCLOUD_PROJECT" "$resolver" 2>/dev/null
  )"; then
    continue
  fi

  if [[ -n "${payload_by_serial[$candidate_serial]+present}" ]] &&
    [ "${payload_by_serial[$candidate_serial]}" != "$normalized_inputs" ]; then
    fail "Historical Terraform state is ambiguous at one serial."
  fi
  payload_by_serial[$candidate_serial]="$normalized_inputs"

  if ((candidate_serial > best_serial)); then
    best_serial="$candidate_serial"
    best_payload="$normalized_inputs"
  fi
done <<<"$version_listing"

[ "$best_serial" -ge 0 ] ||
  fail "No valid historical deployment_inputs were found for this preview state."

printf '%s\n' "$best_payload"
