#!/bin/sh

set -eu

ROOT_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "project foundation contract failed: $*" >&2
  exit 1
}

[ -f terraform/foundation/main.tf ] || fail "standalone foundation root is missing"
grep -Eq 'source[[:space:]]*=[[:space:]]*"https://github.com/libops/cloud-compose/archive/[0-9a-f]{40}\.tar\.gz//cloud-compose-[0-9a-f]{40}/modules/gcp-foundation\?archive=tar\.gz"' terraform/foundation/main.tf ||
  fail "foundation does not pin libops/cloud-compose by immutable commit"
grep -q 'resource "google_artifact_registry_repository" "internal"' terraform/foundation/main.tf ||
  fail "foundation does not own the pre-build Artifact Registry repository"
grep -q 'resource "google_project_iam_custom_role" "vault_gcp_auth_key_verifier"' terraform/foundation/main.tf ||
  fail "foundation does not own the Vault verifier role"
grep -q 'resource "google_project_iam_custom_role" "cloud_compose_observe"' terraform/foundation/main.tf ||
  fail "foundation does not own the production no-suspend role"

foundation_state="$(sed -n '/^data "terraform_remote_state" "shared_foundation" {/,/^}/p' terraform/main.tf)"
printf '%s\n' "$foundation_state" | grep -Eq 'prefix[[:space:]]*=[[:space:]]*local\.foundation_state_prefix' ||
  fail "application workspaces do not consume the standalone foundation state"
if printf '%s\n' "$foundation_state" | grep -q 'workspace'; then
  fail "foundation state must use its isolated default workspace, not an application workspace"
fi
if grep -q '^module "cloud_compose_foundation"' terraform/main.tf; then
  fail "an application workspace still owns project foundation resources"
fi
if grep -q '^resource "google_project_iam_custom_role" "vault_gcp_auth_key_verifier"' terraform/vault.tf; then
  fail "an application workspace still owns the Vault verifier role"
fi

grep -q 'data\.terraform_remote_state\.shared_foundation\.outputs\.cloud_compose_production_observe_role' terraform/main.tf ||
  fail "production does not consume the no-suspend role"
grep -q 'local\.is_prod_workspace ? try(' terraform/main.tf ||
  fail "the no-suspend role is not selected only for production"

foundation_job="$(sed -n '/^  foundation:/,/^  [a-zA-Z0-9_-]*:/p' .github/workflows/terraform-apply.yaml)"
printf '%s\n' "$foundation_job" | grep -q 'prefix=scribe-foundation' ||
  fail "protected delivery does not initialize the standalone foundation state"
printf '%s\n' "$foundation_job" | grep -q 'Plan or apply singleton project foundation' ||
  fail "protected delivery does not reconcile the foundation"
printf '%s\n' "$foundation_job" | grep -q "github.event_name == 'push'.*'apply'.*inputs.mode" ||
  fail "a plan-only dispatch can mutate the foundation"
printf '%s\n' "$foundation_job" | grep -q -- '-lock-timeout=5m' ||
  fail "foundation operations do not use bounded state-lock waiting"
grep -Eq '^  build-ocr:|needs: \[prepare-backend-origin, lint-test, foundation\]' .github/workflows/terraform-apply.yaml ||
  fail "OCR builds are not ordered after the registry foundation"

for job in build-backend build-frontend build-ocr; do
  job_block="$(sed -n "/^  ${job}:/,/^  [a-zA-Z0-9_-]*:/p" .github/workflows/terraform-apply.yaml)"
  printf '%s\n' "$job_block" | grep -Fq "if: github.event_name == 'push' || inputs.mode == 'apply'" ||
    fail "manual plan can execute the ${job} publisher"
done
plan_job="$(sed -n '/^  terraform-plan:/,$p' .github/workflows/terraform-apply.yaml)"
printf '%s\n' "$plan_job" | grep -Fq "if: github.event_name == 'workflow_dispatch' && inputs.mode == 'plan'" ||
  fail "manual plan is not isolated in its read-only caller job"
printf '%s\n' "$plan_job" | grep -q 'needs.resolve-plan-inputs.outputs.api_image' ||
  fail "manual plan does not replay the current immutable backend digest"
if printf '%s\n' "$plan_job" | grep -q 'frontend_image_tag:'; then
  fail "manual plan still depends on the delivery-only frontend GHCR source image"
fi
apply_job="$(sed -n '/^  terraform-apply:/,/^  terraform-plan:/p' .github/workflows/terraform-apply.yaml)"
printf '%s\n' "$apply_job" | grep -q 'frontend_image_tag:.*needs.build-frontend.outputs.image' ||
  fail "protected apply does not pass the reviewed frontend GHCR digest to promotion"
promotion_block="$(sed -n '/Promote the reviewed frontend digest to GAR/,/run: docker buildx imagetools create/p' .github/workflows/terraform-deploy.yaml)"
printf '%s\n' "$promotion_block" | grep -Fq "if: inputs.mode == 'apply'" ||
  fail "manual plan can promote a registry tag"

# Terraform stores only artifacts consumed by runtime resources. The reviewed
# frontend GHCR image remains a delivery input used to promote the deployed GAR
# digest and must not leak back into Terraform variables, state, or replay.
for terraform_file in terraform/main.tf terraform/variables.tf terraform/outputs.tf; do
  if rg -q '(^|[^[:alnum:]_])frontend_image([^[:alnum:]_]|$)' "$terraform_file"; then
    fail "$terraform_file retains the unused frontend_image Terraform input"
  fi
done
if rg -q -- '-var=frontend_image=' terraform/deploy-local.sh; then
  fail "local deployment still injects the unused frontend GHCR image into Terraform"
fi
for replay_file in ci/resolve-destroy-inputs.sh ci/resolve-rollback-inputs.sh ci/fixtures/deployment-inputs.json; do
  if rg -q '(^|[^[:alnum:]_])frontend_image([^[:alnum:]_]|$)' "$replay_file"; then
    fail "$replay_file still records or requires the delivery-only frontend GHCR image"
  fi
done

# App state dependencies are one-way: prod owns shared OCR/Vault; dev may read
# prod, and previews may read dev/prod. No application state may own or feed the
# pre-build foundation, so a clean project has no dev<->prod cycle.
grep -Eq 'shared_ollama_workspace[[:space:]]*=[[:space:]]*"prod"' terraform/main.tf ||
  fail "the shared OCR owner is not explicit"
grep -Eq 'shared_vault_workspace[[:space:]]*=[[:space:]]*local\.is_prod_workspace[[:space:]]*\?[[:space:]]*"prod"[[:space:]]*:[[:space:]]*"dev"' terraform/main.tf ||
  fail "the shared Vault dependency direction is not explicit"

echo "standalone project foundation contracts passed."
