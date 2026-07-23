#!/usr/bin/env bash

set -eu

ROOT_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "Vault first-bootstrap contract failed: $*" >&2
  exit 1
}

job_block="$(sed -n '/^resource "google_cloud_run_v2_job" "vault-init" {/,/^}/p' terraform/modules/vault-cloud-run/main.tf)"
printf '%s\n' "$job_block" | grep -Eq 'run_execution_token[[:space:]]*=[[:space:]]*"run-once-created"' ||
  fail "the init job does not wait for its first execution to complete"
if printf '%s\n' "$job_block" | grep -q 'start_execution_token'; then
  fail "start_execution_token acknowledges launch rather than successful initialization"
fi
printf '%s\n' "$job_block" | grep -Eq 'service_account[[:space:]]*=[[:space:]]*google_service_account\.init\.email' ||
  fail "the initialization job does not use its init-only service account"

grep -Eq '^resource "google_storage_bucket_iam_member" "runtime_data"' terraform/modules/vault-cloud-run/main.tf ||
  fail "the Vault runtime has no explicit data-bucket-only grant"
grep -Eq '^resource "google_storage_bucket_iam_member" "initializer_key"' terraform/modules/vault-cloud-run/main.tf ||
  fail "the Vault initializer has no key-bucket grant"
if sed -n '/^resource "google_storage_bucket_iam_member" "runtime_data" {/,/^}/p' terraform/modules/vault-cloud-run/main.tf | grep -q 'vault\["key"\]'; then
  fail "the Vault runtime can read initialization material"
fi
grep -Eq '^check "vault_runtime_and_initializer_are_distinct"' terraform/modules/vault-cloud-run/main.tf ||
  fail "runtime/init identity separation is not asserted"

kms_moved_file="terraform/modules/vault-cloud-run/moved.tf"
[ -f "$kms_moved_file" ] || fail "Vault runtime KMS state moves are missing"
[ "$(grep -c '^moved {$' "$kms_moved_file")" -eq 2 ] ||
  fail "Vault runtime KMS state must contain exactly two legacy moves"
for role in roles/cloudkms.viewer roles/cloudkms.cryptoKeyEncrypterDecrypter; do
  grep -Fq "from = google_kms_crypto_key_iam_member.vault[\"${role}\"]" "$kms_moved_file" ||
    fail "legacy Vault KMS ${role} state has no source move"
  grep -Fq "to   = google_kms_crypto_key_iam_member.vault_runtime[\"${role}\"]" "$kms_moved_file" ||
    fail "legacy Vault KMS ${role} state has no runtime destination move"
done

url_output="$(sed -n '/^output "vault-url" {/,/^}/p' terraform/modules/vault-cloud-run/outputs.tf)"
printf '%s\n' "$url_output" | grep -Eq 'depends_on[[:space:]]*=[[:space:]]*\[google_cloud_run_v2_job\.vault-init\]' ||
  fail "the Vault URL can be consumed before the initialization job succeeds"
grep -Eq 'vault_url[[:space:]]*=[[:space:]]*local\.vault_is_owner_workspace[[:space:]]*\?[[:space:]]*module\.vault\[0\]\.vault-url' terraform/main.tf ||
  fail "the root Vault provider address no longer consumes the gated module output"

# Exercise the real deploy helper with an empty owner state. Fakes reject any
# Vault address/login/root-token access before the targeted module apply has
# returned, so reordering bootstrap ahead of init fails this test.
TEST_DIR="$(mktemp -d)"
trap 'rm -rf "$TEST_DIR"' EXIT
mkdir -p "$TEST_DIR/bin" "$TEST_DIR/backups"
TERRAFORM_LOG="$TEST_DIR/terraform.log"
BOOTSTRAP_READY="$TEST_DIR/vault-init-complete"

cat >"$TEST_DIR/bin/terraform" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$TF_TEST_LOG"
case "${1:-}" in
  init|validate) exit 0 ;;
  workspace) exit 0 ;;
  state)
    [ "${2:-}" = "list" ] || exit 2
    # An empty owner workspace is the contract under test.
    exit 0
    ;;
  plan)
    [ -s "$TF_BOOTSTRAP_READY" ] || { echo "full plan preceded Vault init" >&2; exit 91; }
    [ "${VAULT_TOKEN:-}" = "test-root-token" ] || { echo "full plan did not receive recovered root token" >&2; exit 92; }
    for argument in "$@"; do
      case "$argument" in
        -out=*)
          printf 'saved-plan\n' >"${argument#-out=}"
          printf '%s\n' "${argument#-out=}" >"$TF_SAVED_PLAN_RECORD"
          ;;
      esac
    done
    printf 'full-plan-complete\n' >>"$TF_TEST_LOG"
    exit 0
    ;;
  show)
    [ "${2:-}" = "-json" ] || { echo "unexpected terraform show: $*" >&2; exit 2; }
    [ "${3:-}" = "$(cat "$TF_SAVED_PLAN_RECORD")" ] || { echo "guard inspected a different plan" >&2; exit 93; }
    printf '%s\n' '{"format_version":"1.2","resource_changes":[{"address":"module.scribe.module.gcp[0].google_compute_disk.data","change":{"actions":["update"]}}]}'
    ;;
  apply)
    if [[ " $* " == *" -target=module.vault "* ]]; then
      [ -z "${VAULT_TOKEN:-}" ] || { echo "root token existed before Vault init" >&2; exit 90; }
      printf 'complete\n' >"$TF_BOOTSTRAP_READY"
      printf 'target-apply-complete\n' >>"$TF_TEST_LOG"
      exit 0
    fi
    [ -s "$TF_BOOTSTRAP_READY" ] || { echo "full apply preceded Vault init" >&2; exit 91; }
    [ "${VAULT_TOKEN:-}" = "test-root-token" ] || { echo "full apply did not receive recovered root token" >&2; exit 92; }
    [ "${!#}" = "$(cat "$TF_SAVED_PLAN_RECORD")" ] || { echo "full apply did not consume the guarded saved plan" >&2; exit 93; }
    printf 'full-apply-complete\n' >>"$TF_TEST_LOG"
    exit 0
    ;;
  *) echo "unexpected terraform invocation: $*" >&2; exit 2 ;;
esac
EOF

cat >"$TEST_DIR/bin/gcloud" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[ -s "$TF_BOOTSTRAP_READY" ] || { echo "Vault access preceded the initialized service shell: $*" >&2; exit 93; }
case "${1:-} ${2:-}" in
  "run services")
    # JWT auth is not configured until the full owner apply, forcing the
    # protected stored-root bootstrap after initialization.
    if [ -z "${VAULT_TOKEN:-}" ]; then
      exit 1
    fi
    printf 'https://vault.example.test\n'
    ;;
  "auth print-access-token")
    [ "${VAULT_TOKEN:-}" = "test-root-token" ] || exit 96
    printf 'test-admin-access-token\n'
    ;;
  "storage cp")
    printf 'dGVzdC1yb290LXRva2Vu\n' >"${4}"
    ;;
  "kms decrypt")
    ciphertext=""
    plaintext=""
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --ciphertext-file) ciphertext="$2"; shift 2 ;;
        --plaintext-file) plaintext="$2"; shift 2 ;;
        *) shift ;;
      esac
    done
    cp "$ciphertext" "$plaintext"
    ;;
  *) echo "unexpected gcloud invocation: $*" >&2; exit 2 ;;
esac
EOF

cat >"$TEST_DIR/bin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = "symbolic-ref" ]; then
  printf 'main\n'
  exit 0
fi
echo "unexpected git invocation: $*" >&2
exit 2
EOF

cat >"$TEST_DIR/bin/docker" <<'EOF'
#!/usr/bin/env bash
echo "Docker must not resolve an already digest-pinned bootstrap image" >&2
exit 94
EOF

cat >"$TEST_DIR/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[ -s "$TF_BOOTSTRAP_READY" ] || exit 95
[ "${VAULT_TOKEN:-}" = "test-root-token" ] || exit 96
args=" $* "
[[ "$args" == *" -H X-Vault-Token: test-root-token "* ]] || exit 97
[[ "$args" == *" -H X-Admin-Token: test-admin-access-token "* ]] || exit 98
[[ "$args" == *" https://vault.example.test/v1/auth/token/lookup-self "* ]] || exit 99
exit 0
EOF

for command in terraform gcloud git docker curl; do
  chmod +x "$TEST_DIR/bin/$command"
done
for command in bash base64 cat cp dirname grep jq mktemp rm sed awk tail tr; do
  ln -s "$(command -v "$command")" "$TEST_DIR/bin/$command"
done

cat >"$TEST_DIR/backups/bootstrap-state.json" <<'EOF'
{
  "versioning": {"enabled": true},
  "softDeletePolicy": {"retentionDurationSeconds": "1209600s"}
}
EOF

digest_a="$(printf 'a%.0s' $(seq 1 64))"
digest_b="$(printf 'b%.0s' $(seq 1 64))"
PATH="$TEST_DIR/bin" \
  GCLOUD_PROJECT=example-project \
  TF_STATE_BUCKET=bootstrap-state \
  BACKUP_AUDIT_FIXTURE_DIR="$TEST_DIR/backups" \
  SCRIBE_API_IMAGE="ghcr.io/example/scribe@sha256:${digest_a}" \
  SCRIBE_FRONTEND_GAR_IMAGE="us-docker.pkg.dev/example-project/internal/scribe-frontend@sha256:${digest_a}" \
  SCRIBE_OCR_IMAGES_JSON="{\"segmentor\":\"us-docker.pkg.dev/example-project/internal/segmentor@sha256:${digest_b}\"}" \
  VAULT_BOOTSTRAP_MODE=jwt-or-root-token \
  TF_TEST_LOG="$TERRAFORM_LOG" \
  TF_BOOTSTRAP_READY="$BOOTSTRAP_READY" \
  TF_SAVED_PLAN_RECORD="$TEST_DIR/saved-plan-path" \
  "$ROOT_DIR/terraform/deploy-local.sh" prod apply \
    --branch aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa >/dev/null

target_line="$(grep -n '^target-apply-complete$' "$TERRAFORM_LOG" | cut -d: -f1)"
plan_line="$(grep -n '^full-plan-complete$' "$TERRAFORM_LOG" | cut -d: -f1)"
full_line="$(grep -n '^full-apply-complete$' "$TERRAFORM_LOG" | cut -d: -f1)"
[ -n "$target_line" ] && [ -n "$plan_line" ] && [ -n "$full_line" ] \
  && [ "$target_line" -lt "$plan_line" ] && [ "$plan_line" -lt "$full_line" ] ||
  fail "deploy helper did not complete target/init/root-token/saved-plan/full-apply in order"

echo "Vault first-bootstrap contracts passed."
