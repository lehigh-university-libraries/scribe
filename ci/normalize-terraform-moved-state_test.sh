#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
fixture="$repo_root/terraform/tests/moved-state-normalizer"
test_root=$(mktemp -d)
build_dir="$test_root/build"
work_dir="$test_root/work"
trap 'rm -rf -- "$test_root"' EXIT
mkdir -p -- "$build_dir" "$work_dir"

prepare_work_dir() {
  local destination=$1

  mkdir -p -- "$destination/.terraform/modules/cloud-compose-fixture"
  cp -- "$build_dir/terraform.tfstate" "$destination/terraform.tfstate"
  cp -- "$fixture/cloud_compose_moved.tf" "$destination/cloud_compose_moved.tf"
  cp -- "$fixture/scribe_root_moved.tf" "$destination/scribe_root_moved.tf"
  cp -- "$fixture/application_network_moved.tf" "$destination/application_network_moved.tf"
  cp -R -- "$fixture/installed/." "$destination/.terraform/modules/cloud-compose-fixture/"
  cp -- "$fixture/modules.json" "$destination/.terraform/modules/modules.json"
}

assert_conflict_is_safe() {
  local destination=$1
  local expected_message=$2
  local before after

  before=$(terraform -chdir="$destination" state pull)
  if "$repo_root/ci/normalize-terraform-moved-state.sh" "$destination" >"$destination/conflict.log" 2>&1; then
    printf 'expected moved-state conflict was accepted\n' >&2
    exit 1
  fi
  after=$(terraform -chdir="$destination" state pull)
  [[ "$before" == "$after" ]] || {
    printf 'moved-state conflict changed state\n' >&2
    exit 1
  }
  grep -Fq "$expected_message" "$destination/conflict.log"
}

cp -- "$fixture/main.tf" "$build_dir/main.tf"
cp -R -- "$fixture/legacy" "$build_dir/legacy"
terraform -chdir="$build_dir" init -backend=false -input=false >/dev/null
terraform -chdir="$build_dir" apply -auto-approve -input=false >/dev/null
prepare_work_dir "$work_dir"
terraform -chdir="$work_dir" state rm \
  terraform_data.root_unindexed_conflict_seed \
  'terraform_data.root_indexed_conflict_seed[0]' >/dev/null

"$repo_root/ci/normalize-terraform-moved-state.sh" "$work_dir" >"$work_dir/first.log"

mapfile -t actual < <(terraform -chdir="$work_dir" state list)
expected=(
  'terraform_data.application_network'
  'terraform_data.application_subnetwork'
  'terraform_data.root_app_policy[0]'
  'terraform_data.root_app_role[0]'
  'terraform_data.root_app_viewer'
  'terraform_data.root_instance_viewer'
  'module.scribe.module.gcp[0].terraform_data.omitted_from_repository_moves'
  'module.scribe.module.gcp[0].terraform_data.repository_counted[0]'
  'module.scribe.module.gcp[0].terraform_data.root[0]'
  'module.scribe.module.gcp[0].module.ppb[0].terraform_data.nested'
)
if [[ "${actual[*]}" != "${expected[*]}" ]]; then
  printf 'unexpected normalized state:\n' >&2
  printf '  %s\n' "${actual[@]}" >&2
  exit 1
fi

before=$(terraform -chdir="$work_dir" state pull)
"$repo_root/ci/normalize-terraform-moved-state.sh" "$work_dir" >"$work_dir/second.log"
after=$(terraform -chdir="$work_dir" state pull)
[[ "$before" == "$after" ]] || {
  printf 'idempotent normalization changed state on its second run\n' >&2
  exit 1
}
grep -q 'Already normalized' "$work_dir/second.log"

# A workspace may already have completed the historical root-module and
# counted-module hops before this root-ownership phase is introduced. Prove
# those exact current cloud-compose addresses still converge to the same root
# destinations without a provider refresh.
counted_dir="$test_root/counted-current"
prepare_work_dir "$counted_dir"
terraform -chdir="$counted_dir" state rm \
  terraform_data.root_unindexed_conflict_seed \
  'terraform_data.root_indexed_conflict_seed[0]' >/dev/null
"$repo_root/ci/normalize-terraform-moved-state.sh" "$counted_dir" >"$counted_dir/historical-normalize.log"
terraform -chdir="$counted_dir" state mv \
  terraform_data.application_network \
  'module.scribe.module.gcp[0].terraform_data.application_network[0]' >/dev/null
terraform -chdir="$counted_dir" state mv \
  terraform_data.application_subnetwork \
  'module.scribe.module.gcp[0].terraform_data.application_subnetwork[0]' >/dev/null
"$repo_root/ci/normalize-terraform-moved-state.sh" "$counted_dir" >"$counted_dir/normalize.log"
mapfile -t counted_actual < <(terraform -chdir="$counted_dir" state list)
if [[ "${counted_actual[*]}" != "${expected[*]}" ]]; then
  printf 'unexpected current-counted normalized state:\n' >&2
  printf '  %s\n' "${counted_actual[@]}" >&2
  exit 1
fi

# A destination collision must fail before any historical address is changed.
# Exercise every source layer that can feed the final root-owned network move;
# assert_conflict_is_safe compares the complete state byte-for-byte.
for lineage in legacy uncounted counted-pre-inner counted-final; do
  conflict_dir="$test_root/application-network-${lineage}-conflict"
  prepare_work_dir "$conflict_dir"
  terraform -chdir="$conflict_dir" state rm \
    'terraform_data.root_indexed_conflict_seed[0]' >/dev/null
  terraform -chdir="$conflict_dir" state mv \
    terraform_data.root_unindexed_conflict_seed \
    terraform_data.application_network >/dev/null
  case "$lineage" in
    legacy) ;;
    uncounted)
      terraform -chdir="$conflict_dir" state mv \
        module.scribe.terraform_data.application_network \
        module.scribe.module.gcp.terraform_data.application_network >/dev/null
      ;;
    counted-pre-inner)
      terraform -chdir="$conflict_dir" state mv \
        module.scribe.terraform_data.application_network \
        'module.scribe.module.gcp[0].terraform_data.application_network' >/dev/null
      ;;
    counted-final)
      terraform -chdir="$conflict_dir" state mv \
        module.scribe.terraform_data.application_network \
        'module.scribe.module.gcp[0].terraform_data.application_network[0]' >/dev/null
      ;;
  esac
  assert_conflict_is_safe "$conflict_dir" \
    'application-network preflight conflict: source lineage'
done

# Multiple historical aliases are equally unsafe even when the root
# destination is absent. Reject the mixed lineage before normalizing either.
alias_conflict_dir="$test_root/application-network-alias-conflict"
prepare_work_dir "$alias_conflict_dir"
terraform -chdir="$alias_conflict_dir" state rm \
  'terraform_data.root_indexed_conflict_seed[0]' >/dev/null
terraform -chdir="$alias_conflict_dir" state mv \
  terraform_data.root_unindexed_conflict_seed \
  module.scribe.module.gcp.terraform_data.application_network >/dev/null
assert_conflict_is_safe "$alias_conflict_dir" \
  'application-network preflight conflict: source aliases'

viewer_conflict_dir="$test_root/viewer-conflict"
prepare_work_dir "$viewer_conflict_dir"
terraform -chdir="$viewer_conflict_dir" state mv \
  terraform_data.root_unindexed_conflict_seed \
  terraform_data.root_app_viewer >/dev/null
terraform -chdir="$viewer_conflict_dir" state rm \
  'terraform_data.root_indexed_conflict_seed[0]' >/dev/null
assert_conflict_is_safe "$viewer_conflict_dir" \
  'repository-root move conflicts: source terraform_data.root_app_viewer[0]'

policy_conflict_dir="$test_root/policy-conflict"
prepare_work_dir "$policy_conflict_dir"
terraform -chdir="$policy_conflict_dir" state mv \
  'terraform_data.root_indexed_conflict_seed[0]' \
  'terraform_data.root_app_policy[0]' >/dev/null
terraform -chdir="$policy_conflict_dir" state rm \
  terraform_data.root_unindexed_conflict_seed >/dev/null
assert_conflict_is_safe "$policy_conflict_dir" \
  'repository-root move conflicts: source terraform_data.root_app_policy'

printf 'Terraform moved-state normalizer acceptance test passed.\n'
