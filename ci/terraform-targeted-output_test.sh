#!/bin/sh

set -eu

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
FIXTURE_DIR="${ROOT_DIR}/terraform/tests/targeted-output"
TEST_DIR="$(mktemp -d)"
MIGRATION_DIR="$(mktemp -d)"

cleanup() {
  rm -rf "$TEST_DIR" "$MIGRATION_DIR"
}
trap cleanup EXIT

cp "$FIXTURE_DIR/after/main.tf" "$TEST_DIR/main.tf"
terraform -chdir="$TEST_DIR" init -backend=false -input=false >/dev/null

# A recovery target must not manufacture a deployment record in an older
# workspace that does not have the lifecycle-managed snapshot yet.
terraform -chdir="$TEST_DIR" apply -auto-approve -input=false \
  -target=terraform_data.maintenance \
  -var=maintenance_revision=one \
  -var=recorded_value=unreviewed >/dev/null
test "$(terraform -chdir="$TEST_DIR" output -json)" = '{}'

# A full apply commits the reviewed snapshot.
terraform -chdir="$TEST_DIR" apply -auto-approve -input=false \
  -var=maintenance_revision=one \
  -var=recorded_value=reviewed >/dev/null
test "$(terraform -chdir="$TEST_DIR" output -raw deployment_inputs)" = reviewed

# Existing workspaces keep their last direct root output while adopting the
# lifecycle-managed snapshot during a recovery-only target.
cp "$FIXTURE_DIR/before/main.tf" "$MIGRATION_DIR/main.tf"
terraform -chdir="$MIGRATION_DIR" init -backend=false -input=false >/dev/null
terraform -chdir="$MIGRATION_DIR" apply -auto-approve -input=false \
  -var=maintenance_revision=one \
  -var=recorded_value=legacy-reviewed >/dev/null
cp "$FIXTURE_DIR/after/main.tf" "$MIGRATION_DIR/main.tf"
migration_plan="$MIGRATION_DIR/target.tfplan"
terraform -chdir="$MIGRATION_DIR" plan -input=false -out="$migration_plan" \
  -target=terraform_data.maintenance \
  -var=maintenance_revision=two \
  -var=recorded_value=unreviewed >/dev/null
terraform -chdir="$MIGRATION_DIR" apply -auto-approve -input=false "$migration_plan" >/dev/null
test "$(terraform -chdir="$MIGRATION_DIR" output -raw deployment_inputs)" = legacy-reviewed

# Later recovery-only work cannot rewrite that snapshot with partial inputs.
plan_file="$TEST_DIR/target.tfplan"
plan_json="$TEST_DIR/target.json"
terraform -chdir="$TEST_DIR" plan -input=false -out="$plan_file" \
  -target=terraform_data.maintenance \
  -var=maintenance_revision=two \
  -var=recorded_value=unreviewed >/dev/null
terraform -chdir="$TEST_DIR" show -json "$plan_file" >"$plan_json"
jq -e '(.output_changes.deployment_inputs.actions // ["no-op"]) == ["no-op"]' "$plan_json" >/dev/null
terraform -chdir="$TEST_DIR" apply -auto-approve -input=false "$plan_file" >/dev/null
test "$(terraform -chdir="$TEST_DIR" output -raw deployment_inputs)" = reviewed

printf '%s\n' '{
  "format_version":"1.2",
  "resource_changes":[
    {"address":"module.vault[0].google_storage_bucket_iam_member.bootstrap_key_reader[\"preview@example.iam.gserviceaccount.com\"]","change":{"actions":["create"]}},
    {"address":"vault_gcp_auth_backend.gcp[0]","change":{"actions":["create"]}},
    {"address":"vault_jwt_auth_backend_role.ci[\"preview\"]","change":{"actions":["create"]}},
    {"address":"data.google_project.current","change":{"actions":["read"]}}
  ],
  "resource_drift":[],
  "output_changes":{"deployment_inputs":{"actions":["no-op"]}}
}' | "$ROOT_DIR/ci/verify-vault-target-plan.sh" vault-ci-identities

if printf '%s\n' '{
  "format_version":"1.2",
  "resource_changes":[
    {"address":"module.scribe.google_compute_instance.app","change":{"actions":["update"]}}
  ],
  "resource_drift":[],
  "output_changes":{}
}' | "$ROOT_DIR/ci/verify-vault-target-plan.sh" vault-ci-identities 2>/dev/null; then
  echo "Vault CI plan verifier accepted an unrelated infrastructure change." >&2
  exit 1
fi

if printf '%s\n' '{
  "format_version":"1.2",
  "resource_changes":[
    {"address":"vault_policy.app[0]","change":{"actions":["update"]}}
  ],
  "resource_drift":[],
  "output_changes":{}
}' | "$ROOT_DIR/ci/verify-vault-target-plan.sh" vault-ci-identities 2>/dev/null; then
  echo "Vault CI plan verifier accepted a dependency-closure mutation." >&2
  exit 1
fi

if printf '%s\n' '{
  "format_version":"1.2",
  "resource_changes":[
    {"address":"vault_gcp_auth_backend.gcp[0]","change":{"actions":["delete"]}}
  ],
  "resource_drift":[],
  "output_changes":{}
}' | "$ROOT_DIR/ci/verify-vault-target-plan.sh" vault-ci-identities 2>/dev/null; then
  echo "Vault CI plan verifier accepted a destructive identity mutation." >&2
  exit 1
fi

if printf '%s\n' '{
  "format_version":"1.2",
  "resource_changes":[
    {"address":"module.vault[0].google_storage_bucket_iam_member.runtime_data","change":{"actions":["update"]}}
  ],
  "resource_drift":[],
  "output_changes":{}
}' | "$ROOT_DIR/ci/verify-vault-target-plan.sh" vault-ci-identities 2>/dev/null; then
  echo "Vault CI plan verifier accepted a broad Vault module mutation." >&2
  exit 1
fi

if printf '%s\n' '{
  "format_version":"1.2",
  "resource_changes":[
    {"address":"vault_gcp_auth_backend.gcp_evil[0]","change":{"actions":["create"]}}
  ],
  "resource_drift":[],
  "output_changes":{}
}' | "$ROOT_DIR/ci/verify-vault-target-plan.sh" vault-ci-identities 2>/dev/null; then
  echo "Vault CI plan verifier accepted a lookalike identity address." >&2
  exit 1
fi

if printf '%s\n' '{
  "format_version":"1.2",
  "resource_changes":[],
  "resource_drift":[],
  "output_changes":{"deployment_inputs":{"actions":["update"]}}
}' | "$ROOT_DIR/ci/verify-vault-target-plan.sh" vault-ci-identities 2>/dev/null; then
  echo "Vault CI plan verifier accepted a recorded output change." >&2
  exit 1
fi

printf '%s\n' '{
  "format_version":"1.2",
  "resource_changes":[
    {"address":"vault_gcp_auth_backend.gcp[0]","change":{"actions":["no-op"]}},
    {"address":"vault_policy.preview_app[0]","change":{"actions":["create"]}},
    {"address":"vault_gcp_auth_backend_role.preview_app[0]","change":{"actions":["update"]}},
    {"address":"data.google_project.current","change":{"actions":["read"]}}
  ],
  "resource_drift":[],
  "output_changes":{"deployment_inputs":{"actions":["no-op"]}}
}' | "$ROOT_DIR/ci/verify-vault-target-plan.sh" vault-preview-runtime

if printf '%s\n' '{
  "format_version":"1.3",
  "resource_changes":[
    {"address":"vault_policy.preview_app[0]","change":{"actions":["no-op"]}},
    {"address":"vault_gcp_auth_backend_role.preview_app[0]","change":{"actions":["no-op"]}}
  ],
  "resource_drift":[],
  "output_changes":{}
}' | "$ROOT_DIR/ci/verify-vault-target-plan.sh" vault-preview-runtime 2>/dev/null; then
  echo "Preview Vault runtime verifier accepted an unknown plan JSON format." >&2
  exit 1
fi

if printf '%s\n' '{
  "format_version":"1.2",
  "resource_changes":[
    {
      "address":"vault_policy.preview_app[0]",
      "previous_address":"vault_policy.break_glass[0]",
      "change":{"actions":["no-op"]}
    },
    {"address":"vault_gcp_auth_backend_role.preview_app[0]","change":{"actions":["no-op"]}}
  ],
  "resource_drift":[],
  "output_changes":{}
}' | "$ROOT_DIR/ci/verify-vault-target-plan.sh" vault-preview-runtime 2>/dev/null; then
  echo "Preview Vault runtime verifier accepted a state move into an allowed address." >&2
  exit 1
fi

if printf '%s\n' '{
  "format_version":"1.2",
  "resource_changes":[
    {
      "address":"vault_policy.preview_app[0]",
      "generated_config":"resource \"vault_policy\" \"preview_app\" {}",
      "change":{"actions":["create"],"importing":{"id":"preview"}}
    },
    {"address":"vault_gcp_auth_backend_role.preview_app[0]","change":{"actions":["no-op"]}}
  ],
  "resource_drift":[],
  "output_changes":{}
}' | "$ROOT_DIR/ci/verify-vault-target-plan.sh" vault-preview-runtime 2>/dev/null; then
  echo "Preview Vault runtime verifier accepted an import into an allowed address." >&2
  exit 1
fi

if printf '%s\n' '{
  "format_version":"1.2",
  "resource_changes":[
    {
      "address":"vault_policy.preview_app[0]",
      "generated_config":"resource \"vault_policy\" \"preview_app\" {}",
      "change":{"actions":["create"]}
    },
    {"address":"vault_gcp_auth_backend_role.preview_app[0]","change":{"actions":["no-op"]}}
  ],
  "resource_drift":[],
  "output_changes":{}
}' | "$ROOT_DIR/ci/verify-vault-target-plan.sh" vault-preview-runtime 2>/dev/null; then
  echo "Preview Vault runtime verifier accepted generated configuration." >&2
  exit 1
fi

if printf '%s\n' '{
  "format_version":"1.2",
  "resource_changes":[
    {"address":"vault_policy.preview_app[0]","change":{"actions":["no-op"]}},
    {"address":"vault_gcp_auth_backend_role.preview_app[0]","change":{"actions":["no-op"]}}
  ],
  "resource_drift":[
    {
      "address":"vault_policy.unrelated_new[0]",
      "previous_address":"vault_policy.unrelated_old[0]",
      "change":{"actions":["no-op"]}
    }
  ],
  "output_changes":{}
}' | "$ROOT_DIR/ci/verify-vault-target-plan.sh" vault-preview-runtime 2>/dev/null; then
  echo "Preview Vault runtime verifier accepted an unrelated state move in resource drift." >&2
  exit 1
fi

if printf '%s\n' '{
  "format_version":"1.2",
  "resource_changes":[
    {"address":"vault_policy.preview_app[0]","change":{"actions":["no-op"]}}
  ],
  "resource_drift":[],
  "output_changes":{}
}' | "$ROOT_DIR/ci/verify-vault-target-plan.sh" vault-preview-runtime 2>/dev/null; then
  echo "Preview Vault runtime verifier accepted a plan missing the runtime role." >&2
  exit 1
fi

if printf '%s\n' '{
  "format_version":"1.2",
  "resource_changes":[
    {"address":"vault_gcp_auth_backend.gcp[0]","change":{"actions":["update"]}},
    {"address":"vault_policy.preview_app[0]","change":{"actions":["no-op"]}},
    {"address":"vault_gcp_auth_backend_role.preview_app[0]","change":{"actions":["no-op"]}}
  ],
  "resource_drift":[],
  "output_changes":{}
}' | "$ROOT_DIR/ci/verify-vault-target-plan.sh" vault-preview-runtime 2>/dev/null; then
  echo "Preview Vault runtime verifier accepted an auth-backend dependency mutation." >&2
  exit 1
fi

if printf '%s\n' '{
  "format_version":"1.2",
  "resource_changes":[
    {"address":"vault_policy.preview_app[0]","change":{"actions":["delete"]}},
    {"address":"vault_gcp_auth_backend_role.preview_app[0]","change":{"actions":["no-op"]}}
  ],
  "resource_drift":[],
  "output_changes":{}
}' | "$ROOT_DIR/ci/verify-vault-target-plan.sh" vault-preview-runtime 2>/dev/null; then
  echo "Preview Vault runtime verifier accepted a destructive policy mutation." >&2
  exit 1
fi

if printf '%s\n' '{"format_version":"1.2"}' |
  "$ROOT_DIR/ci/verify-vault-target-plan.sh" unreviewed-scope 2>/dev/null; then
  echo "Vault target verifier accepted an unknown maintenance scope." >&2
  exit 1
fi

printf '%s\n' 'Targeted maintenance preserves lifecycle-managed root outputs.'
