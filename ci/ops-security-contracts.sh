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

# Every workflow has an explicit least-privilege token baseline. Jobs may opt
# into narrow writes, but no job can inherit repository-default writes.
while IFS= read -r workflow; do
  require_pattern '^permissions:$' "$workflow"
  forbid_pattern '^permissions:[[:space:]]*write-all$' "$workflow"
done < <(find .github/workflows -maxdepth 1 -type f \( -name '*.yaml' -o -name '*.yml' \) -print)
bash ci/docs-workflow-contract_test.sh
matrix_job="$(sed -n '/^  matrix:/,/^  build:/p' .github/workflows/build-ocr.yaml)"
rg -q '^    permissions:$' <<<"$matrix_job" || fail "OCR matrix job must have explicit permissions"
rg -q '^      contents: read$' <<<"$matrix_job" || fail "OCR matrix job must be explicitly read-only"
if rg -q 'id-token:[[:space:]]*write|packages:[[:space:]]*write' <<<"$matrix_job"; then
  fail "OCR matrix job must not inherit publisher credentials"
fi

# Production may reuse OCR digests only after a protected, full-history state
# reader proves that no image input changed and revalidates the recorded map.
production_ocr_detector="$(sed -n '/^  ocr-change-detection:/,/^  [a-zA-Z0-9_-]*:/p' .github/workflows/terraform-apply.yaml)"
rg -q '^    environment: production$' <<<"$production_ocr_detector" ||
  fail "OCR change detection must be bound to the production environment"
rg -q '^      id-token: write$' <<<"$production_ocr_detector" ||
  fail "OCR change detection must use explicit deploy WIF credentials"
rg -q '^      contents: read$' <<<"$production_ocr_detector" ||
  fail "OCR change detection must receive only read access to repository contents"
if rg -q 'packages:[[:space:]]*write' <<<"$production_ocr_detector"; then
  fail "OCR change detection must not receive registry-write credentials"
fi
# shellcheck disable=SC2016 # Match literal workflow shell variables.
for detector_contract in \
  'fetch-depth: 0' \
  'terraform_wrapper: false' \
  'WIF_EXPECTED_ENVIRONMENT: production' \
  'WIF_IDENTITY_CLASS: deploy' \
  'terraform -chdir=terraform workspace select prod' \
  'terraform -chdir=terraform output -json deployment_inputs' \
  './ci/resolve-rollback-inputs.sh -' \
  'git merge-base --is-ancestor "$previous_sha" "$CURRENT_SHA"' \
  './ci/ocr-source-paths.sh' \
  'git diff --name-only "$previous_sha" "$CURRENT_SHA" -- "${ocr_source_path_list[@]}"' \
  './ci/select-current-ocr-images.sh' \
  'rebuild_ocr "the production deployment record is unreadable"' \
  'rebuild_ocr "the previous deployment commit is outside checkout history"' \
  'rebuild_ocr "the deployment history could not be compared"' \
  'rebuild_ocr "recorded OCR digests do not cover the current service matrix"'; do
  rg -Fq -- "$detector_contract" <<<"$production_ocr_detector" ||
    fail "OCR change detection omits protected reuse contract: $detector_contract"
done
production_ocr_build="$(sed -n '/^  build-ocr:/,/^  [a-zA-Z0-9_-]*:/p' .github/workflows/terraform-apply.yaml)"
rg -Fq "needs.ocr-change-detection.outputs.ocr_changed == 'true'" <<<"$production_ocr_build" ||
  fail "the production OCR publisher is not gated by source changes"
rg -q '^    needs: \[[^]]*ocr-change-detection[^]]*\]$' <<<"$production_ocr_build" ||
  fail "the production OCR publisher does not depend on change detection"
production_apply_job="$(sed -n '/^  terraform-apply:/,/^  terraform-plan:/p' .github/workflows/terraform-apply.yaml)"
for apply_contract in \
  'always() &&' \
  "needs.ocr-change-detection.result == 'success'" \
  "needs.ocr-change-detection.outputs.ocr_changed == 'true'" \
  "needs.build-ocr.result == 'success'" \
  "needs.build-ocr.result == 'skipped'" \
  "needs.ocr-change-detection.outputs.ocr_changed == 'false'" \
  "ocr_images_json: \${{ needs.ocr-change-detection.outputs.ocr_changed == 'true' && needs.build-ocr.outputs.images_json || needs.ocr-change-detection.outputs.ocr_images_json }}"; do
  rg -Fq -- "$apply_contract" <<<"$production_apply_job" ||
    fail "production apply omits OCR skip/reuse contract: $apply_contract"
done

# Untrusted preview images may only run under a no-data identity that has no
# OCR invoker grant. OCR deep probes and compute containers use separate
# identities and must not share application permissions.
require_pattern 'service_account = google_service_account\.backend_readiness\.email' terraform/readiness.tf
require_pattern 'service_account = google_service_account\.ocr_readiness\.email' terraform/readiness.tf
require_pattern 'member   = "serviceAccount:\$\{google_service_account\.ocr_readiness\.email\}"' terraform/kraken.tf
forbid_pattern 'google_service_account\.backend_readiness\.email' terraform/kraken.tf
forbid_pattern 'service_account = module\.scribe\.appGsa\.email' terraform/readiness.tf
require_pattern 'readiness_network_resource_name = regex\(' terraform/readiness.tf
require_pattern 'projects/\[\^/\]\+/global/networks/\[\^/\]\+\$' terraform/readiness.tf
require_pattern 'readiness_subnetwork_resource_name = regex\(' terraform/readiness.tf
require_pattern 'projects/\[\^/\]\+/regions/\[\^/\]\+/subnetworks/\[\^/\]\+\$' terraform/readiness.tf
test "$(rg -c 'network    = local\.readiness_network_resource_name' terraform/readiness.tf)" -eq 2 ||
  fail "both readiness jobs must use the canonical Cloud Run network resource name"
test "$(rg -c 'subnetwork = local\.readiness_subnetwork_resource_name' terraform/readiness.tf)" -eq 2 ||
  fail "both readiness jobs must use the canonical Cloud Run subnetwork resource name"
forbid_pattern 'network    = module\.scribe\.network\.self_link' terraform/readiness.tf
forbid_pattern 'subnetwork = module\.scribe\.network\.subnetwork' terraform/readiness.tf
require_pattern 'readiness_jobs = \{' terraform/monitoring.tf
require_pattern 'backend = try\(google_cloud_run_v2_job\.backend_readiness\[0\]\.name, ""\)' terraform/monitoring.tf
require_pattern 'ocr[[:space:]]+= try\(google_cloud_run_v2_job\.ocr_readiness\[0\]\.name, ""\)' terraform/monitoring.tf
require_pattern 'for_each = local\.is_prod_workspace \? local\.readiness_jobs : \{\}' terraform/monitoring.tf
forbid_pattern 'readiness_job_names|for_each = .*toset\(' terraform/monitoring.tf

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
bash ci/ocr-local-defaults-contract_test.sh
bash ci/cloud-ocr-compose-preflight_test.sh
bash ci/run-ci-network-contract_test.sh
bash ci/configure-dev-cloud-ocr_test.sh
bash ci/dev-external-ocr-iam-contract_test.sh
bash ci/segmentor-lock-check.sh
bash ci/tool-version-contract_test.sh
bash ci/toolchain-check_test.sh
bash ci/update-env_test.sh
bash ci/verify-gcp-wif_test.sh
bash ci/bootstrap-external-gcp-identities_test.sh
bash ci/gcp-wif-workflow-contract_test.sh
bash ci/capture-command-log_test.sh
bash ci/gcp-vm-bootstrap-diagnostics_test.sh
bash ci/run-cloud-run-readiness_test.sh
bash ci/preview-deployment-evidence-contract_test.sh

# Outbound Cloud Run callers must share the credential-file-aware source.
# A direct HTR metadata source would hang behind the intentional VM metadata
# firewall and turn image ingestion into a request-time failure.
identity_source_imports="$(rg -l 'htr/pkg/auth/gcpidtoken' internal --glob '*.go' || true)"
test "$identity_source_imports" = "internal/gcpidentity/source.go" ||
  fail "only internal/gcpidentity may import HTR's metadata identity source"
require_pattern 'preflightServiceIdentity\(ctx, cfg\)' internal/app/bootstrap.go

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
bash ci/compose-network-ipam-contract_test.sh
bash ci/cos-host-jq-portability_test.sh
bash ci/compose-runtime-preflight_test.sh
bash ci/traefik-v3-rules-contract_test.sh
bash ci/deployment-replay-contract_test.sh
bash ci/verify-production-persistent-disk-plan_test.sh
for volume in mariadb-data uploads-data cache-data triplet-presentation-data triplet-cache-data; do
  require_pattern "SCRIBE_DATA_GENERATION:-canonical-v2}-${volume}" docker-compose.yaml
done
require_pattern 'name  = "SCRIBE_DATA_GENERATION"' terraform/main.tf
require_pattern 'value = "uploads/\$\{var\.data_generation\}"' terraform/main.tf
forbid_pattern 'SCRIBE_UPLOADS_BUCKET|SCRIBE_UPLOADS_PREFIX' terraform/kraken.tf
require_pattern 'each\.key.*transcription-jobs' terraform/main.tf
require_pattern 'transcription_jobs_forward\[var\.data_generation\]' terraform/main.tf
require_pattern 'data_generation[[:space:]]*=[[:space:]]*var\.data_generation' terraform/outputs.tf
require_pattern 'SCRIBE_DATA_GENERATION: canonical-v2' .github/workflows/terraform-deploy.yaml
# shellcheck disable=SC2016 # This is an rg pattern, not an expanding shell expression.
require_pattern 'SCRIBE_DATA_GENERATION="\$\(jq -r '\''\.data_generation'\'' <<<"\$previous"\)"' .github/workflows/terraform-deploy.yaml
require_pattern '^[[:space:]]*export[[:space:]].*SCRIBE_DATA_GENERATION([[:space:]]|$)' .github/workflows/terraform-deploy.yaml
require_pattern 'SCRIBE_DATA_GENERATION=.*data_generation' .github/workflows/terraform-drift.yaml
# The destroy fixture deliberately remains on the recorded prior generation.
require_pattern 'data_generation.*canonical-v1' ci/deploy-local-destroy_test.sh
require_pattern 'data_generation.*test' ci/resolve-destroy-inputs.sh

# The runtime app identity may materialize only the application database
# credential. MariaDB's local root bootstrap secret never leaves the VM and is
# not readable through the app Vault policy.
bash ci/vault-app-policy-contract_test.sh
bash ci/vault-database-path-contract_test.sh
bash ci/generate-secrets-permissions_test.sh
bash ci/vault-init-image-contract_test.sh
bash ci/vault-init-diagnostics_test.sh
bash ci/vault-first-bootstrap-contract_test.sh
bash ci/preview-vault-runtime-contract_test.sh
bash ci/project-foundation-contract_test.sh
bash ci/backup-verifier-iam-contract_test.sh
bash ci/preview-vault-secrets_test.sh
bash ci/vault-policy-capabilities_test.sh
bash ci/vault-retry_test.sh
bash ci/vault-terraform-readiness_test.sh
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
require_pattern '^[[:space:]]+set -eu$' ci/test.sh
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
require_pattern 'resolve-oci-platform-image\.sh' ci/attest-cloud-run-revision.sh
require_pattern 'reviewed frontend index or its exact linux/amd64 image' ci/attest-cloud-run-revision.sh
require_pattern 'SCRIBE_EXPECTED_API_IMAGE' terraform/readiness.tf
require_pattern '/api/generate' scripts/ocr-readiness.sh
require_pattern 'ollama_readiness_invoker' terraform/ollama.tf
require_pattern 'readiness\.api_image !== expectedAPIImage' web/readiness-job.mjs
require_pattern 'frontend-image-smoke\.sh' .github/workflows/terraform-apply.yaml
require_pattern 'frontend-image-smoke\.sh' .github/workflows/terraform-preview.yaml
require_pattern 'vault-init-image-smoke\.sh' .github/workflows/terraform-apply.yaml
require_pattern '^  publish-images:' .github/workflows/terraform-preview.yaml
require_pattern 'run: \./ci/publish-preview-images\.sh' .github/workflows/terraform-preview.yaml
require_pattern 'oci-archive:/images/scribe-backend\.oci\.tar' ci/publish-preview-images.sh
require_pattern 'oci-archive:/images/scribe-frontend\.oci\.tar' ci/publish-preview-images.sh
require_pattern 'mktemp -d -- .*scribe-preview-publish\.XXXXXXXXXX' ci/publish-preview-images.sh
require_pattern '\$\{work_dir\}:/var/tmp:rw' ci/publish-preview-images.sh
forbid_pattern '--tmpfs .*var/tmp' ci/publish-preview-images.sh
require_pattern 'quay\.io/skopeo/stable:v[0-9.]+@sha256:[0-9a-f]{64}' .github/workflows/terraform-preview.yaml
bash ci/publish-preview-images_test.sh
require_pattern 'artifacts docker tags list' ci/resolve-gar-image.sh
forbid_pattern 'artifacts docker images describe|containeranalysis' ci/resolve-gar-image.sh
bash ci/resolve-gar-image_test.sh
require_pattern 'terraform output -json deployment_inputs' terraform/deploy-local.sh
require_pattern 'resolve-destroy-inputs\.sh' terraform/deploy-local.sh
require_pattern 'recover-preview-destroy-inputs\.sh' terraform/deploy-local.sh
require_pattern 'recover-destroy' .github/workflows/terraform-preview.yaml
require_pattern 'recover_preview_destroy_inputs' .github/workflows/terraform-deploy.yaml
forbid_pattern 'state push|storage cp[^\n]*gs://[^\n]*gs://' ci/recover-preview-destroy-inputs.sh
require_pattern 'deployment-status\.sh' .github/workflows/terraform-deploy.yaml
require_pattern 'VAULT_BOOTSTRAP_MODE: root-token' .github/workflows/terraform-drift.yaml
forbid_pattern 'vault-login\.sh|Authenticate Google ID token for Vault' .github/workflows/terraform-drift.yaml
require_pattern '::add-mask::%s' terraform/deploy-local.sh
require_pattern 'image_tag: \$\{\{ github\.sha \}\}' .github/workflows/terraform-apply.yaml
require_pattern 'api_image must be a digest-pinned GHCR reference' terraform/variables.tf
require_pattern 'Every ocr_service_images value must be a digest-pinned image reference' terraform/variables.tf
forbid_pattern 'secrets\.OCR_(GCLOUD_OIDC_POOL|GSA)' .github/workflows/build-ocr.yaml
forbid_pattern '\$\{\{ (secrets|vars)\.GSA \}\}' .github/workflows/build-ocr.yaml
require_pattern 'service_account: \$\{\{ vars\.OCR_GSA \}\}' .github/workflows/build-ocr.yaml
require_pattern 'run: make dependency-scan' .github/workflows/lint-test.yaml
require_pattern '--scanners "vuln,secret"' ci/dependency-scan.sh
require_pattern 'Create a synthetic credential scanner fixture' .github/workflows/lint-test.yaml
bash ci/dependency-scan-path-parity_test.sh
forbid_pattern 'IMAGE_TAG=main' .github/workflows/terraform-drift.yaml
bash ci/release-tag_test.sh
bash ci/release-draft_test.sh
bash ci/release-coverage_test.sh
bash ci/release-workflow-security-check_test.sh
bash ci/verify-release-source-run_test.sh
require_pattern 'token: "\{\{ \.Env\.HOMEBREW_REPO_TOKEN \}\}"' .goreleaser.yaml
require_pattern 'draft: true' .goreleaser.yaml
require_pattern 'use_existing_draft: true' .goreleaser.yaml
require_pattern 'replace_existing_artifacts: true' .goreleaser.yaml

# Pull-request head code is built and smoked without a registry-write token.
# The protected publisher consumes OCI archives without executing the head.
# OCR images come only from the exact protected base revision already deployed
# to production; the preview resolver can read state but cannot publish images.
preview_build_permissions="$(sed -n '/^  build-backend:/,/^  publish-images:/p' .github/workflows/terraform-preview.yaml)"
if rg -q 'packages: write|docker/login-action' <<<"$preview_build_permissions"; then
  fail "preview head build jobs must not receive registry-write credentials"
fi
forbid_pattern '^  build-ocr:' .github/workflows/terraform-preview.yaml
preview_ocr_resolver="$(sed -n '/^  resolve-production-ocr-images:/,/^  deploy:/p' .github/workflows/terraform-preview.yaml)"
rg -q '^    needs: \[prepare, lint-test\]$' <<<"$preview_ocr_resolver" ||
  fail "preview OCR resolution must wait for trusted input resolution and PR-head CI"
rg -q '^    environment: preview$' <<<"$preview_ocr_resolver" ||
  fail "preview OCR resolution must be protected by the preview environment"
rg -q '^      id-token: write$' <<<"$preview_ocr_resolver" ||
  fail "preview OCR resolution must use the preview deploy WIF identity"
rg -q '^      contents: read$' <<<"$preview_ocr_resolver" ||
  fail "preview OCR resolution must receive read-only repository access"
if rg -q 'packages:[[:space:]]*write|OCR_GCLOUD_OIDC_POOL|OCR_GSA' <<<"$preview_ocr_resolver"; then
  fail "preview OCR resolution must not receive OCR publisher credentials"
fi
# shellcheck disable=SC2016 # Match literal workflow expressions and shell variables.
for preview_ocr_contract in \
  'group: terraform-deploy-scribe' \
  'queue: max' \
  'ref: ${{ needs.prepare.outputs.base_sha }}' \
  'WIF_EXPECTED_ENVIRONMENT: preview' \
  'WIF_IDENTITY_CLASS: deploy' \
  'terraform -chdir=terraform workspace select prod' \
  'terraform -chdir=terraform output -json deployment_inputs' \
  './ci/resolve-rollback-inputs.sh -' \
  '[ "$(jq -r '\''.configuration.project_id'\'' <<<"$deployed")" = "$GCLOUD_PROJECT" ]' \
  '[ "$(jq -r '\''.docker_compose_sha'\'' <<<"$deployed")" = "$BASE_SHA" ]' \
  'WORKSPACE_SLUG="pr-${PR_NUMBER}"' \
  'INCLUDE_OLLAMA=false' \
  './ci/select-current-ocr-images.sh'; do
  rg -Fq -- "$preview_ocr_contract" <<<"$preview_ocr_resolver" ||
    fail "preview OCR resolution omits protected production replay contract: $preview_ocr_contract"
done
preview_deploy_job="$(sed -n '/^  deploy:/,/^  destroy:/p' .github/workflows/terraform-preview.yaml)"
rg -q '^    needs: \[[^]]*resolve-production-ocr-images[^]]*\]$' <<<"$preview_deploy_job" ||
  fail "preview deploy does not wait for protected OCR resolution"
# shellcheck disable=SC2016 # Match the literal GitHub expression.
rg -Fq 'ocr_images_json: ${{ needs.resolve-production-ocr-images.outputs.images_json }}' <<<"$preview_deploy_job" ||
  fail "preview deploy does not consume the validated production OCR digest map"
for preview_deploy_job in \
  "$preview_deploy_job" \
  "$(sed -n '/^  destroy:/,/^  record-preview-deployment:/p' .github/workflows/terraform-preview.yaml)"; do
  rg -q '^      packages: read$' <<<"$preview_deploy_job" ||
    fail "preview deploy and destroy callers must satisfy the reusable workflow package-read contract"
done

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
done < <(rg -n '^[[:space:]]*-?[[:space:]]*uses:[[:space:]]*' .github/workflows --glob '*.yaml' --glob '*.yml')

echo "Ops/security static contracts passed."
