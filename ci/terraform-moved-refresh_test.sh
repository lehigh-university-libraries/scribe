#!/bin/sh

set -eu

ROOT_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
FIXTURE_DIR="${ROOT_DIR}/terraform/tests/moved-refresh"
TEST_DIR="$(mktemp -d)"
trap 'rm -rf "${TEST_DIR}"' EXIT HUP INT TERM

cp "${FIXTURE_DIR}/before/main.tf" "${TEST_DIR}/main.tf"
cp -R "${FIXTURE_DIR}/modules" "${TEST_DIR}/modules"

terraform -chdir="${TEST_DIR}" init -backend=false -input=false >/dev/null
terraform -chdir="${TEST_DIR}" apply -auto-approve -input=false >/dev/null

initial_state_list="$(terraform -chdir="${TEST_DIR}" state list)"
for legacy_address in \
  'terraform_data.legacy' \
  'module.scribe.module.gcp[0].terraform_data.application_network[0]' \
  'module.scribe.module.gcp[0].terraform_data.application_subnetwork[0]'; do
  if ! printf '%s\n' "${initial_state_list}" | grep -Fx "${legacy_address}" >/dev/null; then
    echo "Moved-resource fixture did not create ${legacy_address}." >&2
    exit 1
  fi
done

cp "${FIXTURE_DIR}/after/main.tf" "${TEST_DIR}/main.tf"

# Prove the fixture reproduces the maintenance failure this normalization is
# intended to prevent: Terraform refuses to target an unrelated resource while
# an unapplied moved block still needs to update another state address.
if terraform -chdir="${TEST_DIR}" plan \
  -input=false \
  -target=terraform_data.maintenance \
  >"${TEST_DIR}/target-before.out" 2>&1; then
  echo "Targeted plan unexpectedly accepted the unnormalized legacy address." >&2
  exit 1
fi
if ! grep -F 'Moved resource instances excluded by targeting' "${TEST_DIR}/target-before.out" >/dev/null; then
  echo "Targeted plan failed for an unexpected reason before normalization." >&2
  sed -n '1,80p' "${TEST_DIR}/target-before.out" >&2
  exit 1
fi

refresh_plan="${TEST_DIR}/refresh.tfplan"
terraform -chdir="${TEST_DIR}" plan \
  -input=false \
  -refresh-only \
  -out="${refresh_plan}" >/dev/null

terraform -chdir="${TEST_DIR}" show -no-color "${refresh_plan}" >"${TEST_DIR}/refresh-plan.out"
for expected_move in \
  'terraform_data.legacy has moved to terraform_data.normalized' \
  'module.scribe.module.gcp[0].terraform_data.application_network[0] has moved to terraform_data.application_network' \
  'module.scribe.module.gcp[0].terraform_data.application_subnetwork[0] has moved to terraform_data.application_subnetwork'; do
  if ! grep -F "${expected_move}" "${TEST_DIR}/refresh-plan.out" >/dev/null; then
    echo "Saved refresh-only plan did not contain the expected move: ${expected_move}" >&2
    sed -n '1,120p' "${TEST_DIR}/refresh-plan.out" >&2
    exit 1
  fi
done

terraform -chdir="${TEST_DIR}" apply -input=false "${refresh_plan}" >/dev/null

state_list="$(terraform -chdir="${TEST_DIR}" state list)"
for normalized_address in \
  'terraform_data.normalized' \
  'terraform_data.application_network' \
  'terraform_data.application_subnetwork'; do
  if ! printf '%s\n' "${state_list}" | grep -Fx "${normalized_address}" >/dev/null; then
    echo "Refresh-only apply did not persist ${normalized_address}." >&2
    exit 1
  fi
done
for legacy_address in \
  'terraform_data.legacy' \
  'module.scribe.module.gcp[0].terraform_data.application_network[0]' \
  'module.scribe.module.gcp[0].terraform_data.application_subnetwork[0]'; do
  if printf '%s\n' "${state_list}" | grep -Fx "${legacy_address}" >/dev/null; then
    echo "Refresh-only apply left ${legacy_address} in state." >&2
    exit 1
  fi
done

if ! terraform -chdir="${TEST_DIR}" plan \
  -detailed-exitcode \
  -input=false \
  >"${TEST_DIR}/full-after.out" 2>&1; then
  echo "Full plan found drift after refresh-only moved-resource normalization." >&2
  sed -n '1,120p' "${TEST_DIR}/full-after.out" >&2
  exit 1
fi

if ! terraform -chdir="${TEST_DIR}" plan \
  -detailed-exitcode \
  -input=false \
  -target=terraform_data.maintenance \
  >"${TEST_DIR}/target-after.out" 2>&1; then
  echo "Targeted maintenance plan failed after moved-resource normalization." >&2
  sed -n '1,80p' "${TEST_DIR}/target-after.out" >&2
  exit 1
fi
if grep -F 'Moved resource instances excluded by targeting' "${TEST_DIR}/target-after.out" >/dev/null; then
  echo "Targeted maintenance plan still encountered an unapplied state move." >&2
  exit 1
fi

echo "Saved refresh-only plans normalize moved addresses before targeted maintenance."
