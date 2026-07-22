#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "ops/security contract failed: $*" >&2
  exit 1
}

require_pattern() {
  local pattern="$1"
  local file="$2"
  rg -q -- "$pattern" "$file" || fail "$file is missing required pattern: $pattern"
}

forbid_pattern() {
  local pattern="$1"
  local file="$2"
  if rg -q -- "$pattern" "$file"; then
    fail "$file contains forbidden pattern: $pattern"
  fi
}

# Every workflow has an explicit read-only token baseline. Jobs may opt into a
# narrow write permission, but no job can inherit repository-default writes.
while IFS= read -r workflow; do
  require_pattern '^permissions:$' "$workflow"
  forbid_pattern '^permissions:[[:space:]]*write-all$' "$workflow"
done < <(find .github/workflows -maxdepth 1 -type f \( -name '*.yaml' -o -name '*.yml' \) -print)
matrix_job="$(sed -n '/^  matrix:/,/^  build:/p' .github/workflows/build-ocr.yaml)"
rg -q '^    permissions:$' <<<"$matrix_job" || fail "OCR matrix job must have explicit permissions"
rg -q '^      contents: read$' <<<"$matrix_job" || fail "OCR matrix job must be explicitly read-only"
if rg -q 'id-token:[[:space:]]*write|packages:[[:space:]]*write' <<<"$matrix_job"; then
  fail "OCR matrix job must not inherit publisher credentials"
fi

# Untrusted preview images may only run under a no-data identity that has no
# OCR invoker grant. OCR deep probes and compute containers use separate
# identities and must not share application permissions.
require_pattern 'service_account = google_service_account\.backend_readiness\.email' terraform/readiness.tf
require_pattern 'service_account = google_service_account\.ocr_readiness\.email' terraform/readiness.tf
require_pattern 'member   = "serviceAccount:\$\{google_service_account\.ocr_readiness\.email\}"' terraform/kraken.tf
forbid_pattern 'google_service_account\.backend_readiness\.email' terraform/kraken.tf
forbid_pattern 'service_account = module\.scribe\.appGsa\.email' terraform/readiness.tf

# The proxy must forward Vault KV requests from app and bootstrap identities.
# Vault itself still requires a token and applies the workspace-scoped policy.
require_pattern '"/v1/secret/"' terraform/vault.tf
for vault_script in ci/vault-login.sh ci/vault-secrets.sh; do
  # shellcheck disable=SC2016 # This is an rg pattern, not an expanding shell expression.
  forbid_pattern 'cat "\$response_file" >&2|echo "\$VAULT_LAST_RESPONSE" >&2' "$vault_script"
  require_pattern 'response body withheld because it may contain sensitive' "$vault_script"
  require_pattern '--connect-timeout 5' "$vault_script"
  require_pattern '--max-time 30' "$vault_script"
done
forbid_pattern 'sys/policies/acl|sys/auth|sys/mounts|sys/audit|auth/google-jwt|auth/gcp/role' terraform/policies/vault/ci.hcl
forbid_pattern ':latest' terraform/modules/vault-cloud-run/main.tf
require_pattern 'local\.vault_image_context_sha' terraform/modules/vault-cloud-run/main.tf
require_pattern 'vault_image_context_sha = sha256' terraform/modules/vault-cloud-run/main.tf
forbid_pattern 'filesha1|= sha1\(' terraform/modules/vault-cloud-run/main.tf
require_pattern 'pip_spec: "kraken==7\.0\.2"' config/ocr.yaml
require_pattern 'sha256: "77a638a83c9e535620827a09e410ed36391e9e8e8126d5796a0f15b978186056"' config/ocr.yaml
require_pattern 'https://download\.pytorch\.org/whl/cpu' config/segmentor-requirements.in
forbid_pattern 'nvidia-cuda|whl/cu[0-9]' Dockerfile.segmentor
bash ci/segmentor-lock-check.sh
bash ci/tool-version-contract_test.sh
bash ci/toolchain-check_test.sh
bash ci/update-env_test.sh
bash ci/verify-gcp-wif_test.sh
bash ci/gcp-wif-workflow-contract_test.sh
bash ci/preview-deployment-evidence-contract_test.sh

# Proxy identity is a two-hop exact allowlist: Cloud Run's deployment subnet
# into Traefik, then Traefik's fixed container /32 into the application.
require_pattern 'SERVER_TRUSTED_PROXY_CIDRS:.*172\.30\.0\.2/32' docker-compose.yaml
require_pattern 'trusted_proxy_cidrs:.*SERVER_TRUSTED_PROXY_CIDRS' config.yaml
require_pattern 'ipv4_address:.*172\.30\.0\.2' docker-compose.yaml
require_pattern 'TRAEFIK_FORWARDED_TRUSTED_IPS' terraform/main.tf
require_pattern 'forwardedHeaders\.notAppendXForwardedFor=true' docker-compose.yaml
require_pattern 'ENV SCRIBE_FRONTEND_EDGE_MODE=ppb' Dockerfile.frontend
require_pattern 'power_button_ip_depth[[:space:]]*=[[:space:]]*0' terraform/main.tf
for terraform_file in terraform/main.tf terraform/variables.tf terraform/outputs.tf; do
  forbid_pattern 'app_domain|shared_lb|modules/shared-lb' "$terraform_file"
done
require_pattern 'TF_VAR_network_ip_cidr_range' .github/workflows/terraform-deploy.yaml
require_pattern 'TF_VAR_network_ip_cidr_range' .github/workflows/terraform-drift.yaml
forbid_pattern '10\.0\.0\.0/8|172\.16\.0\.0/12|192\.168\.0\.0/16|169\.254\.0\.0/16|fc00::/7' docker-compose.yaml

# One reviewed generation must isolate every persistence surface. Deployment,
# drift, rollback, and destroy recover it from the immutable state payload.
bash ci/persistence-generation-contract_test.sh
bash ci/deployment-replay-contract_test.sh
for volume in mariadb-data uploads-data cache-data triplet-presentation-data triplet-cache-data; do
  require_pattern "SCRIBE_DATA_GENERATION:-canonical-v1}-${volume}" docker-compose.yaml
done
require_pattern 'name  = "SCRIBE_DATA_GENERATION"' terraform/main.tf
require_pattern 'value = "uploads/\$\{var\.data_generation\}"' terraform/main.tf
forbid_pattern 'SCRIBE_UPLOADS_BUCKET|SCRIBE_UPLOADS_PREFIX' terraform/kraken.tf
require_pattern 'var\.data_generation.*transcription-jobs' terraform/main.tf
require_pattern 'data_generation[[:space:]]*=[[:space:]]*var\.data_generation' terraform/outputs.tf
require_pattern 'SCRIBE_DATA_GENERATION: canonical-v1' .github/workflows/terraform-deploy.yaml
# shellcheck disable=SC2016 # This is an rg pattern, not an expanding shell expression.
require_pattern 'export SCRIBE_DATA_GENERATION="\$\(jq -r '\''\.data_generation'\'' <<<"\$previous"\)"' .github/workflows/terraform-deploy.yaml
require_pattern 'SCRIBE_DATA_GENERATION=.*data_generation' .github/workflows/terraform-drift.yaml
require_pattern 'data_generation.*canonical-v1' ci/deploy-local-destroy_test.sh
require_pattern 'data_generation.*test' ci/resolve-destroy-inputs.sh

# The runtime app identity may materialize only the application database
# credential. MariaDB's local root bootstrap secret never leaves the VM and is
# not readable through the app Vault policy.
bash ci/vault-app-policy-contract_test.sh
bash ci/vault-database-path-contract_test.sh
bash ci/vault-init-image-contract_test.sh
bash ci/vault-first-bootstrap-contract_test.sh
bash ci/preview-vault-runtime-contract_test.sh
bash ci/project-foundation-contract_test.sh
bash ci/backup-verifier-iam-contract_test.sh
bash ci/preview-vault-secrets_test.sh
bash ci/vault-policy-capabilities_test.sh
bash ci/vault-retry_test.sh
require_pattern 'secret/data/scribe/previews/scribe-pr-\*' terraform/policies/vault/ci.hcl
forbid_pattern 'secret/data/scribe/\+/database/app' terraform/policies/vault/ci.hcl
require_pattern 'secret/data/scribe/dev/\*' terraform/policies/vault/ci.hcl
require_pattern 'secret/data/scribe/prod/\*' terraform/policies/vault/ci.hcl
require_pattern 'service_account = google_service_account\.init\.email' terraform/modules/vault-cloud-run/main.tf
forbid_pattern 'google_storage_bucket\.vault\["key"\].*google_service_account\.gsa' terraform/modules/vault-cloud-run/main.tf
require_pattern 'AUTH_PREVIEW_ANONYMOUS' terraform/main.tf
require_pattern 'const stripsCredentials = isSeparateOrigin \|\| isPublicPresentation' web/server.mjs

# The hosted browser acceptance suite must fail rather than silently selecting
# its in-browser persistence fallback. It must also ignore an ambient TEST_DSN:
# the fixture mutates data and is authorized only for the Compose database it
# resolves itself.
require_pattern 'SCRIBE_REQUIRE_BROWSER_BACKEND: "true"' .github/workflows/lint-test.yaml
require_pattern 'Start isolated browser-test database' .github/workflows/lint-test.yaml
forbid_pattern '\$\{TEST_DSN:-' ci/test-browser.sh

# Persistent dependency/build caches may not turn a prior successful database
# test into evidence for a new isolated schema. Package serialization protects
# schema-global assertions while -count=1 bypasses Go's test-result cache.
require_pattern 'go test -p=1 -count=1 -v -race \./\.\.\.' ci/test.sh
require_pattern 'go test -p=1 -count=1 -v ./internal/server ./internal/store' ci/e2e-smoke.sh

# Runtime cost controls advertised in sample.env must be present in both API
# and worker Compose environments, and the two runtime config copies must be
# byte-identical.
for variable in \
  TRANSCRIPTION_MAX_ACTIVE_JOBS_PER_WORKSPACE \
  STORAGE_MAX_BYTES_PER_WORKSPACE STORAGE_MAX_BYTES_TOTAL \
  STORAGE_MAX_ITEMS_PER_WORKSPACE STORAGE_MAX_ITEMS_TOTAL \
  STORAGE_MAX_IMAGES_PER_WORKSPACE STORAGE_MAX_IMAGES_TOTAL \
  STORAGE_RESERVATION_TTL STORAGE_NORMALIZATION_CACHE_MAX_BYTES \
  STORAGE_NORMALIZATION_CACHE_MAX_AGE IIIF_MAX_MANIFEST_CANVASES \
  IIIF_MAX_MANIFEST_IMPORT_BYTES; do
  [ "$(rg -c "^[[:space:]]+${variable}:" docker-compose.yaml)" -eq 2 ] || fail "$variable must be wired to both API and worker"
done
cmp -s config.yaml internal/config/defaults/config.yaml || fail "runtime config.yaml and its embedded copy differ"
require_pattern 'x-scribe-runtime-hardening: &scribe-runtime-hardening' docker-compose.yaml
require_pattern 'no-new-privileges:true' docker-compose.yaml
require_pattern 'http://localhost:8081/readyz' docker-compose.yaml
require_pattern 'triplet-healthcheck.*http://127\.0\.0\.1:8080/healthz' docker-compose.yaml
for service in api worker triplet; do
  require_pattern "^  ${service}:" docker-compose.yaml
done
forbid_pattern '^  image-service:' docker-compose.yaml
[ "$(rg -c 'TRIPLET_SOURCE_READ_TOKEN_FILE: /run/secrets/triplet_source_read_token' docker-compose.yaml)" -eq 2 ] || fail "Triplet source token must be mounted into both API and worker"
require_pattern '^  default: http$' triplet.config.yaml
require_pattern 'prefix: http://api:8080/static/uploads' triplet.config.yaml
require_pattern 'allowed_origins:' triplet.config.yaml
require_pattern '^[[:space:]]+- http://api:8080$' triplet.config.yaml
require_pattern 'auth_probe: true' triplet.config.yaml
forbid_pattern 'prefix: /static/uploads' triplet.config.yaml

# Production recovery and supply-chain controls are executable contracts.
require_pattern 'google_storage_transfer_job.*uploads_backup' terraform/backup.tf
require_pattern 'terraform_state_backup_audited' terraform/backup.tf
require_pattern 'production_vm_snapshots_enabled' terraform/backup.tf
require_pattern 'production[[:space:]]*=[[:space:]]*local\.is_prod_workspace' terraform/main.tf
require_pattern 'cloud-compose-snapshot-contract_test\.sh' ci/terraform-check.sh
require_pattern 'cloud-compose-release-contract_test\.sh' ci/terraform-check.sh
require_pattern 'cloud-snapshot-restore-drill\.sh' .github/workflows/backup-verification.yaml
require_pattern 'BACKUP_RESTORE_GSA' .github/workflows/backup-verification.yaml
require_pattern 'mount -t ext4 -o ro,noload' ci/cloud-snapshot-restore-drill.sh
require_pattern 'mode=ro,boot=no,auto-delete=no' ci/cloud-snapshot-restore-drill.sh
forbid_pattern 'compute (instances|disks) delete.*PRODUCTION_' ci/cloud-snapshot-restore-drill.sh
require_pattern '^output "deployment_inputs"' terraform/outputs.tf
require_pattern 'precondition' terraform/outputs.tf
require_pattern 'Production and preview plans require a non-placeholder compose SHA' terraform/outputs.tf
require_pattern 'Production plans require a live state-backup audit' terraform/outputs.tf
require_pattern 'Production plans require project-local Cloud Monitoring notification channels' terraform/outputs.tf
require_pattern 'Roll back failed production rollout' .github/workflows/terraform-deploy.yaml
require_pattern "steps\.apply\.outcome != 'skipped'" .github/workflows/terraform-deploy.yaml
require_pattern 'Attest deployed Cloud Run revision' .github/workflows/terraform-deploy.yaml
require_pattern 'attest-cloud-run-revision\.sh' .github/workflows/terraform-deploy.yaml
require_pattern 'expected digest-pinned frontend image' ci/attest-cloud-run-revision.sh
require_pattern 'SCRIBE_EXPECTED_API_IMAGE' terraform/readiness.tf
require_pattern '/api/generate' terraform/readiness.tf
require_pattern 'ollama_readiness_invoker' terraform/ollama.tf
require_pattern 'readiness\.api_image !== expectedAPIImage' web/readiness-job.mjs
require_pattern 'frontend-image-smoke\.sh' .github/workflows/terraform-apply.yaml
require_pattern 'frontend-image-smoke\.sh' .github/workflows/terraform-preview.yaml
require_pattern 'vault-init-image-smoke\.sh' .github/workflows/terraform-apply.yaml
require_pattern '^  publish-images:' .github/workflows/terraform-preview.yaml
require_pattern 'oci-archive:/images/scribe-backend\.oci\.tar' .github/workflows/terraform-preview.yaml
require_pattern 'oci-archive:/images/scribe-frontend\.oci\.tar' .github/workflows/terraform-preview.yaml
require_pattern 'quay\.io/skopeo/stable:v[0-9.]+@sha256:[0-9a-f]{64}' .github/workflows/terraform-preview.yaml
require_pattern 'terraform output -json deployment_inputs' terraform/deploy-local.sh
require_pattern 'resolve-destroy-inputs\.sh' terraform/deploy-local.sh
require_pattern 'deployment-status\.sh' .github/workflows/terraform-deploy.yaml
require_pattern 'VAULT_BOOTSTRAP_MODE: root-token' .github/workflows/terraform-drift.yaml
forbid_pattern 'vault-login\.sh|Authenticate Google ID token for Vault' .github/workflows/terraform-drift.yaml
require_pattern '::add-mask::%s' terraform/deploy-local.sh
require_pattern 'image_tag: \$\{\{ github\.sha \}\}' .github/workflows/terraform-apply.yaml
require_pattern 'api_image must be a digest-pinned GHCR reference' terraform/variables.tf
require_pattern 'Every ocr_service_images value must be a digest-pinned image reference' terraform/variables.tf
forbid_pattern 'secrets\.OCR_GSA !=.*secrets\.GSA' .github/workflows/build-ocr.yaml
require_pattern 'service_account: \$\{\{ secrets\.OCR_GSA \}\}' .github/workflows/build-ocr.yaml
require_pattern 'scanners: vuln,secret' .github/workflows/lint-test.yaml
require_pattern 'Scan exact backend runtime image' .github/workflows/terraform-apply.yaml
require_pattern 'Scan exact frontend runtime image' .github/workflows/terraform-apply.yaml
require_pattern 'Scan exact OCR runtime image' .github/workflows/build-ocr.yaml
require_pattern 'Scan exact published preview images' .github/workflows/terraform-preview.yaml
require_pattern 'terraform_data" "vault_image_scan' terraform/modules/vault-cloud-run/main.tf
require_pattern 'Create a synthetic credential scanner fixture' .github/workflows/lint-test.yaml
forbid_pattern 'IMAGE_TAG=main' .github/workflows/terraform-drift.yaml
bash ci/release-tag_test.sh
bash ci/release-draft_test.sh
bash ci/release-coverage_test.sh
bash ci/release-workflow-security-check_test.sh
require_pattern 'token: "\{\{ \.Env\.HOMEBREW_REPO_TOKEN \}\}"' .goreleaser.yaml
require_pattern 'draft: true' .goreleaser.yaml
require_pattern 'use_existing_draft: true' .goreleaser.yaml
require_pattern 'replace_existing_artifacts: true' .goreleaser.yaml

# Pull-request head code is built and smoked without a registry-write token.
# Only the protected publisher job receives packages:write and it consumes OCI
# archives without checking out or executing the pull-request tree.
preview_build_permissions="$(sed -n '/^  build-backend:/,/^  publish-images:/p' .github/workflows/terraform-preview.yaml)"
if rg -q 'packages: write|docker/login-action' <<<"$preview_build_permissions"; then
  fail "preview head build jobs must not receive registry-write credentials"
fi

# Every third-party action is pinned to a full commit SHA. Local reusable
# workflows are deliberately exempt.
while IFS=: read -r file line value; do
  use="${value#*uses: }"
  use="${use%% *}"
  use="${use#\"}"
  use="${use%\"}"
  use="${use#\'}"
  use="${use%\'}"
  case "$use" in
    ./*) continue ;;
  esac
  [[ "$use" =~ ^[^@[:space:]]+@[0-9a-f]{40}$ ]] || fail "$file:$line has a floating action reference: $use"
done < <(rg -n '^[[:space:]]*-?[[:space:]]*uses:[[:space:]]*' .github/workflows --glob '*.yaml')

echo "Ops/security static contracts passed."
