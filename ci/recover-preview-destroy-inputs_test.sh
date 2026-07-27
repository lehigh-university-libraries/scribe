#!/usr/bin/env bash

set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
helper="$root_dir/ci/recover-preview-destroy-inputs.sh"
deployment_fixture="$root_dir/ci/fixtures/deployment-inputs.json"
test_dir="$(mktemp -d)"
trap 'rm -rf -- "$test_dir"' EXIT

mkdir -p "$test_dir/bin" "$test_dir/states"
terraform_log="$test_dir/terraform.log"
gcloud_log="$test_dir/gcloud.log"

cat >"$test_dir/bin/terraform" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$RECOVERY_TERRAFORM_LOG"
[ "$#" -eq 2 ] && [ "$1" = state ] && [ "$2" = pull ] || exit 2
cat "$RECOVERY_CURRENT_STATE"
EOF
chmod +x "$test_dir/bin/terraform"

cat >"$test_dir/bin/gcloud" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$RECOVERY_GCLOUD_LOG"
case "${1:-} ${2:-}" in
  "storage ls")
    [ "${3:-}" = --all-versions ] || exit 2
    [ "${4:-}" = "gs://state-bucket/scribe/pr-88.tfstate" ] || exit 2
    cat "$RECOVERY_LISTING"
    ;;
  "storage cat")
    [ "$#" -eq 3 ] || exit 2
    generation="${3##*#}"
    cat "$RECOVERY_STATES/${generation}.json"
    ;;
  *) exit 2 ;;
esac
EOF
chmod +x "$test_dir/bin/gcloud"

make_state() {
  local destination="$1"
  local serial="$2"
  local lineage="$3"
  local inputs="${4:-}"

  if [ -n "$inputs" ]; then
    jq -n \
      --argjson serial "$serial" \
      --arg lineage "$lineage" \
      --slurpfile inputs "$inputs" '
        {
          version: 4,
          serial: $serial,
          lineage: $lineage,
          outputs: {
            deployment_inputs: {
              value: $inputs[0],
              sensitive: false
            }
          }
        }
      ' >"$destination"
  else
    jq -n \
      --argjson serial "$serial" \
      --arg lineage "$lineage" '
        {version: 4, serial: $serial, lineage: $lineage, outputs: {}}
      ' >"$destination"
  fi
}

run_helper() {
  local stdout_file="$1"
  local stderr_file="$2"

  PATH="$test_dir/bin:$PATH" \
    GCLOUD_PROJECT=scribe-test1 \
    TF_STATE_BUCKET=state-bucket \
    TF_WORKSPACE=pr-88 \
    RECOVERY_CURRENT_STATE="$test_dir/current.json" \
    RECOVERY_LISTING="$test_dir/listing.txt" \
    RECOVERY_STATES="$test_dir/states" \
    RECOVERY_TERRAFORM_LOG="$terraform_log" \
    RECOVERY_GCLOUD_LOG="$gcloud_log" \
    "$helper" >"$stdout_file" 2>"$stderr_file"
}

assert_failed_without_output() {
  local name="$1"

  if run_helper "$test_dir/${name}.out" "$test_dir/${name}.err"; then
    echo "recovery unexpectedly succeeded: $name" >&2
    exit 1
  fi
  [ ! -s "$test_dir/${name}.out" ] || {
    echo "failed recovery exposed output: $name" >&2
    exit 1
  }
}

valid_a="$test_dir/valid-a.json"
valid_b="$test_dir/valid-b.json"
invalid_inputs="$test_dir/invalid-inputs.json"
legacy_inputs="$test_dir/legacy-inputs.json"
cp "$deployment_fixture" "$valid_a"
jq '.data_generation = "canonical-v2"' "$deployment_fixture" >"$valid_b"
jq '.api_image = "ghcr.io/lehigh-university-libraries/scribe:mutable"' \
  "$deployment_fixture" >"$invalid_inputs"
jq 'del(.configuration.dev_external_ocr_impersonators)' \
  "$deployment_fixture" >"$legacy_inputs"

# Select the highest valid lower serial regardless of listing order. A newer
# wrong-lineage state, an equal-serial state, and a malformed deployment input
# payload are all ineligible. The selected legacy payload is normalized by the
# same resolver used by ordinary preview teardown.
make_state "$test_dir/current.json" 10 lineage-a
make_state "$test_dir/states/101.json" 3 lineage-a "$valid_a"
make_state "$test_dir/states/102.json" 8 lineage-a "$legacy_inputs"
make_state "$test_dir/states/103.json" 9 lineage-other "$valid_b"
make_state "$test_dir/states/104.json" 10 lineage-a "$valid_b"
make_state "$test_dir/states/105.json" 9 lineage-a "$invalid_inputs"
printf '%s\n' \
  'gs://state-bucket/scribe/pr-88.tfstate#104' \
  'gs://state-bucket/scribe/pr-88.tfstate#101' \
  'gs://state-bucket/scribe/pr-88.tfstate#105' \
  'gs://state-bucket/scribe/pr-88.tfstate#103' \
  'gs://state-bucket/scribe/pr-88.tfstate#102' >"$test_dir/listing.txt"
: >"$terraform_log"
: >"$gcloud_log"
run_helper "$test_dir/success.out" "$test_dir/success.err"
expected="$(GCLOUD_PROJECT=scribe-test1 \
  "$root_dir/ci/resolve-destroy-inputs.sh" <"$legacy_inputs")"
[ "$(<"$test_dir/success.out")" = "$expected" ]
[ ! -s "$test_dir/success.err" ]
jq -e '.configuration.dev_external_ocr_impersonators == []' \
  "$test_dir/success.out" >/dev/null
[ "$(<"$terraform_log")" = "state pull" ]
[ "$(grep -Fc 'storage ls --all-versions gs://state-bucket/scribe/pr-88.tfstate' "$gcloud_log")" -eq 1 ]
[ "$(grep -Fc 'storage cat gs://state-bucket/scribe/pr-88.tfstate#' "$gcloud_log")" -eq 5 ]
if grep -Eq '^storage (cp|mv|rm|restore)|state push' "$gcloud_log" "$terraform_log"; then
  echo "read-only recovery invoked a state mutation" >&2
  exit 1
fi

# If the current state still has the canonical output, recovery is neither
# needed nor allowed and object history must not be inspected.
make_state "$test_dir/current.json" 10 lineage-a "$valid_a"
: >"$gcloud_log"
assert_failed_without_output current-output-present
[ ! -s "$gcloud_log" ]
grep -F 'already contains deployment_inputs' \
  "$test_dir/current-output-present.err" >/dev/null

# Wrong-lineage snapshots and invalid payloads cannot become teardown inputs.
make_state "$test_dir/current.json" 10 lineage-a
printf '%s\n' \
  'gs://state-bucket/scribe/pr-88.tfstate#103' \
  'gs://state-bucket/scribe/pr-88.tfstate#105' >"$test_dir/listing.txt"
assert_failed_without_output no-valid-history
grep -F 'No valid historical deployment_inputs were found' \
  "$test_dir/no-valid-history.err" >/dev/null

# The exact object path and a decimal generation are mandatory. Do not parse a
# partial or surprising listing and accidentally inspect another state object.
printf '%s\n' \
  'gs://state-bucket/scribe/pr-88.tfstate#101' \
  'gs://state-bucket/scribe/pr-89.tfstate#102' >"$test_dir/listing.txt"
assert_failed_without_output malformed-listing
grep -F 'version listing was malformed' "$test_dir/malformed-listing.err" >/dev/null

printf '%s\n' \
  'gs://state-bucket/scribe/pr-88.tfstate#101' \
  'gs://state-bucket/scribe/pr-88.tfstate#101' >"$test_dir/listing.txt"
assert_failed_without_output duplicate-generation
grep -F 'duplicate generation' "$test_dir/duplicate-generation.err" >/dev/null

# Two validated snapshots at the same Terraform serial must agree exactly.
# Otherwise there is no deterministic historical output to recover.
make_state "$test_dir/states/106.json" 8 lineage-a "$valid_a"
make_state "$test_dir/states/107.json" 8 lineage-a "$valid_b"
printf '%s\n' \
  'gs://state-bucket/scribe/pr-88.tfstate#106' \
  'gs://state-bucket/scribe/pr-88.tfstate#107' >"$test_dir/listing.txt"
assert_failed_without_output ambiguous-serial
grep -F 'ambiguous at one serial' "$test_dir/ambiguous-serial.err" >/dev/null

# Recovery is limited to explicit preview workspaces.
: >"$terraform_log"
: >"$gcloud_log"
if PATH="$test_dir/bin:$PATH" \
  GCLOUD_PROJECT=scribe-test1 \
  TF_STATE_BUCKET=state-bucket \
  TF_WORKSPACE=prod \
  "$helper" >"$test_dir/prod.out" 2>"$test_dir/prod.err"; then
  echo "recovery accepted a non-preview workspace" >&2
  exit 1
fi
[ ! -s "$test_dir/prod.out" ]
[ ! -s "$terraform_log" ]
[ ! -s "$gcloud_log" ]
grep -F 'must identify one preview workspace as pr-N' "$test_dir/prod.err" >/dev/null

echo "preview destroy input recovery contract passed"
