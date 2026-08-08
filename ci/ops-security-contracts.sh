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
bash ci/ensure-local-vault-init-image_test.sh
bash ci/browser-readiness-contract_test.sh
bash ci/production-iap-ssh-contract_test.sh
bash ci/run-production-browser-readiness_test.sh
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
  fail "backend and OCR readiness must use the canonical application network resource name"
test "$(rg -c 'subnetwork = local\.readiness_subnetwork_resource_name' terraform/readiness.tf)" -eq 2 ||
  fail "backend and OCR readiness jobs must use the canonical application subnetwork resource name"
test "$(rg -c 'network    = local\.browser_readiness_network_resource_name' terraform/readiness.tf)" -eq 1 ||
  fail "browser readiness must use only its environment application network resource name"
test "$(rg -c 'subnetwork = local\.browser_readiness_subnetwork_resource_name' terraform/readiness.tf)" -eq 1 ||
  fail "browser readiness must use only its isolated dual-stack subnetwork resource name"
require_pattern 'tags       = \[local\.browser_readiness_network_tag\]' terraform/readiness.tf
require_pattern 'resource "google_compute_firewall" "browser_readiness_isolation"' terraform/readiness.tf
require_pattern 'destination_ranges = \[var\.network_ip_cidr_range\]' terraform/readiness.tf
forbid_pattern 'network    = module\.scribe\.network\.self_link' terraform/readiness.tf
forbid_pattern 'subnetwork = module\.scribe\.network\.subnetwork' terraform/readiness.tf
require_pattern 'readiness_jobs = \{' terraform/monitoring.tf
require_pattern 'backend = try\(google_cloud_run_v2_job\.backend_readiness\[0\]\.name, ""\)' terraform/monitoring.tf
require_pattern 'browser = try\(google_cloud_run_v2_job\.browser_readiness\[0\]\.name, ""\)' terraform/monitoring.tf
require_pattern 'ocr[[:space:]]+= try\(google_cloud_run_v2_job\.ocr_readiness\[0\]\.name, ""\)' terraform/monitoring.tf
require_pattern 'for_each = local\.is_prod_workspace \? \{' terraform/monitoring.tf
require_pattern 'for kind, job in local\.readiness_jobs : kind => job if trimspace\(job\) != ""' terraform/monitoring.tf
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
bash ci/cloud-ocr-compose-preflight_test.sh
bash ci/run-ci-network-contract_test.sh
bash ci/configure-dev-cloud-ocr_test.sh
bash ci/dev-external-ocr-iam-contract_test.sh
bash ci/tool-version-contract_test.sh
bash ci/toolchain-check_test.sh
bash ci/update-env_test.sh
bash ci/verify-gcp-wif_test.sh
bash ci/bootstrap-external-gcp-identities_test.sh
bash ci/gcp-wif-workflow-contract_test.sh
bash ci/capture-command-log_test.sh
bash ci/gcp-vm-bootstrap-diagnostics_test.sh
bash ci/run-cloud-run-readiness_test.sh

# Outbound Cloud Run callers must share the credential-file-aware source.
# A direct HTR metadata source would hang behind the intentional VM metadata
# firewall and turn image ingestion into a request-time failure.
identity_source_imports="$(rg -l 'htr/pkg/auth/gcpidtoken' internal --glob '*.go' || true)"
test "$identity_source_imports" = "internal/gcpidentity/source.go" ||
  fail "only internal/gcpidentity may import HTR's metadata identity source"
require_pattern 'preflightServiceIdentity\(ctx, cfg\)' internal/app/bootstrap.go

# Proxy identity is a two-hop exact allowlist: Cloud Run's deployment subnet
# into Traefik, then the resolved Traefik service identity into the application.
require_pattern 'SERVER_TRUSTED_PROXY_HOSTS:.*traefik' docker-compose.yaml
require_pattern 'trusted_proxy_cidrs:.*SERVER_TRUSTED_PROXY_CIDRS' config.yaml
require_pattern 'trusted_proxy_hosts:.*SERVER_TRUSTED_PROXY_HOSTS' config.yaml
forbid_pattern 'ipv4_address:|SCRIBE_COMPOSE_SUBNET|SCRIBE_TRAEFIK_IP' docker-compose.yaml
require_pattern 'TRAEFIK_FORWARDED_TRUSTED_IPS' terraform/main.tf
require_pattern 'forwardedHeaders\.notAppendXForwardedFor=true' docker-compose.yaml
require_pattern 'ENV SCRIBE_FRONTEND_EDGE_MODE=ppb' Dockerfile.frontend
require_pattern 'power_button_ip_depth[[:space:]]*=[[:space:]]*0' terraform/main.tf
for terraform_file in terraform/main.tf terraform/variables.tf terraform/outputs.tf; do
  forbid_pattern 'app_domain|shared_lb|modules/shared-lb' "$terraform_file"
done
require_pattern 'TF_VAR_network_ip_cidr_range' .github/workflows/terraform-deploy.yaml
require_pattern 'TF_VAR_network_ip_cidr_range' .github/workflows/terraform-drift.yaml
require_pattern 'TF_VAR_browser_readiness_subnet_cidr' .github/workflows/terraform-deploy.yaml
require_pattern 'TF_VAR_browser_readiness_subnet_cidr' .github/workflows/terraform-drift.yaml
for runtime_default_var in \
  transcription_max_active_jobs_per_workspace \
  storage_max_bytes_per_workspace storage_max_bytes_total \
  storage_max_items_per_workspace storage_max_items_total \
  storage_max_images_per_workspace storage_max_images_total \
  storage_reservation_ttl storage_normalization_cache_max_bytes \
  storage_normalization_cache_max_age iiif_max_manifest_canvases \
  iiif_max_manifest_import_bytes; do
  require_pattern "${runtime_default_var}[[:space:]]*=[[:space:]]*coalesce\\(var\\.${runtime_default_var}" terraform/main.tf
done
require_pattern 'application_config[[:space:]]*=[[:space:]]*yamldecode\(file\(' terraform/main.tf
# shellcheck disable=SC2016 # Match the literal GitHub environment-file variable.
require_pattern 'go run ./cmd/deployer runtime-overrides >> "\$GITHUB_ENV"' .github/workflows/terraform-deploy.yaml
require_pattern "if: inputs.mode == 'apply' \\|\\| inputs.mode == 'plan'" .github/workflows/terraform-deploy.yaml
forbid_pattern 'while IFS=: read -r source target' .github/workflows/terraform-deploy.yaml
forbid_pattern 'vars\.(TRANSCRIPTION_MAX_ACTIVE_JOBS_PER_WORKSPACE|STORAGE_MAX_|IIIF_MAX_).*\|\|[[:space:]]*' .github/workflows/terraform-deploy.yaml
forbid_pattern '(TRANSCRIPTION_MAX_ACTIVE_JOBS_PER_WORKSPACE|STORAGE_MAX_|IIIF_MAX_)[^:]*:[[:space:]]*\$\{[^}]+:-[1-9]' docker-compose.yaml
forbid_pattern '^[[:space:]]*(transcription_max_active_jobs_per_workspace|storage_|iiif_max_manifest_)[a-z_]*[[:space:]]*=' terraform/terraform.tfvars.example
require_pattern 'resource "terraform_data" "recorded_root_outputs"' terraform/outputs.tf
require_pattern 'precondition \{' terraform/outputs.tf
require_pattern 'Effective runtime limits must satisfy' terraform/outputs.tf
forbid_pattern '^check "(deterministic_cloud_run_url_available|runtime_limit_relationships)"' terraform/main.tf
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
require_pattern '^run_browser_group\(\)' ci/run-ci.sh
require_pattern 'SCRIBE_REQUIRE_BROWSER_BACKEND=true run_make test-browser' ci/run-ci.sh
forbid_pattern '\$\{TEST_DSN:-' ci/test-browser.sh

# Persistent dependency/build caches may not turn a prior successful database
# test into evidence for a new isolated schema. Package serialization protects
# schema-global assertions while -count=1 bypasses Go's test-result cache.
require_pattern 'go test -p=1 -count=1 -v -race \./\.\.\.' ci/test.sh
require_pattern '^[[:space:]]+set -eu$' ci/test.sh
require_pattern 'SCRIBE_REQUIRE_TEST_DB: "true"' .github/workflows/lint-test.yaml
require_pattern 'SCRIBE_REQUIRE_TEST_DB=true run_make test-backend' ci/run-ci.sh
require_pattern 'REQUIRE_TEST_DB="\$\{SCRIBE_REQUIRE_TEST_DB:-false\}"' ci/test.sh

# Runtime cost controls advertised in sample.env must resolve identically for
# API and worker. Assert the rendered Compose model so a shared YAML anchor is
# checked at the service boundary instead of requiring duplicate source text.
compose_app_config="$(docker compose -f docker-compose.yaml config --format json)"
jq -e '.services.api.environment == .services.worker.environment' \
  <<<"$compose_app_config" >/dev/null ||
  fail "rendered API and worker environments differ"
for variable in \
  TRANSCRIPTION_MAX_ACTIVE_JOBS_PER_WORKSPACE \
  STORAGE_MAX_BYTES_PER_WORKSPACE STORAGE_MAX_BYTES_TOTAL \
  STORAGE_MAX_ITEMS_PER_WORKSPACE STORAGE_MAX_ITEMS_TOTAL \
  STORAGE_MAX_IMAGES_PER_WORKSPACE STORAGE_MAX_IMAGES_TOTAL \
  STORAGE_RESERVATION_TTL STORAGE_NORMALIZATION_CACHE_MAX_BYTES \
  STORAGE_NORMALIZATION_CACHE_MAX_AGE IIIF_MAX_MANIFEST_CANVASES \
  IIIF_MAX_MANIFEST_IMPORT_BYTES; do
  jq -e --arg variable "$variable" '
    .services.api.environment[$variable] != null and
    .services.api.environment[$variable] == .services.worker.environment[$variable]
  ' <<<"$compose_app_config" >/dev/null ||
    fail "$variable must resolve identically for API and worker"
done
managed_observability_config="$(
  SCRIBE_OTEL_EXPORTER=google \
    GOOGLE_CLOUD_PROJECT=scribe-observability-1 \
    SCRIBE_DEPLOYMENT_ENVIRONMENT=prod \
    docker compose -f docker-compose.yaml config --format json
)"
for service in api worker; do
  jq -e --arg service "$service" '
    .services[$service].environment.SCRIBE_OTEL_EXPORTER == "google" and
    .services[$service].environment.GOOGLE_CLOUD_PROJECT == "scribe-observability-1" and
    .services[$service].environment.SCRIBE_DEPLOYMENT_ENVIRONMENT == "prod"
  ' <<<"$managed_observability_config" >/dev/null ||
    fail "managed observability settings do not reach $service"
done
require_pattern 'value = local\.is_preview_workspace \? "none" : "google"' terraform/main.tf
require_pattern 'for_each = local\.is_preview_workspace \? toset\(\[\]\)' terraform/main.tf
cmp -s config.yaml internal/config/defaults/config.yaml || fail "runtime config.yaml and its embedded copy differ"
require_pattern 'x-scribe-runtime-hardening: &scribe-runtime-hardening' docker-compose.yaml
require_pattern 'x-scribe-app-env: &scribe-app-env' docker-compose.yaml
require_pattern 'SCRIBE_API_IMAGE:-scribe-api:local' docker-compose.yaml
for override in docker-compose.override-example.yaml docker-compose.override.cloud-example.yaml; do
  require_pattern 'SCRIBE_FRONTEND_EDGE_MODE:[[:space:]]+direct' "$override"
done
require_pattern 'COPY config.yaml /etc/scribe/config.yaml' Dockerfile
jq -e '[.services.api.volumes[], .services.worker.volumes[]] |
  all(.target != "/etc/scribe/config.yaml")' <<<"$compose_app_config" >/dev/null ||
  fail "Compose overrides the image-baked application config"
require_pattern 'no-new-privileges:true' docker-compose.yaml
require_pattern 'http://localhost:8081/readyz' docker-compose.yaml
require_pattern 'triplet-healthcheck.*http://127\.0\.0\.1:8080/healthz' docker-compose.yaml
for service in api worker triplet; do
  require_pattern "^  ${service}:" docker-compose.yaml
done
forbid_pattern '^  image-service:' docker-compose.yaml
jq -e '
  .services.api.environment.TRIPLET_SOURCE_READ_TOKEN_FILE == "/run/secrets/triplet_source_read_token" and
  .services.worker.environment.TRIPLET_SOURCE_READ_TOKEN_FILE == "/run/secrets/triplet_source_read_token"
' <<<"$compose_app_config" >/dev/null ||
  fail "Triplet source token must be configured for both API and worker"
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
require_pattern 'SCRIBE_EXPECTED_PUBLIC_ORIGIN' terraform/readiness.tf
require_pattern 'value = local\.public_base_url' terraform/readiness.tf
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
require_pattern 'run: \./ci/run-ci\.sh security' .github/workflows/lint-test.yaml
ci_group_count=0
while IFS= read -r ci_group; do
  [ -n "$ci_group" ] || continue
  ci_group_count=$((ci_group_count + 1))
  [ "$(grep -Fxc "        run: ./ci/run-ci.sh ${ci_group}" .github/workflows/lint-test.yaml)" -eq 1 ] ||
    fail "hosted CI must invoke canonical group ${ci_group} exactly once"
done < <(./ci/run-ci.sh --list)
[ "$(rg -c '^[[:space:]]+run: \./ci/run-ci\.sh [a-z]+$' .github/workflows/lint-test.yaml)" -eq "$ci_group_count" ] ||
  fail "hosted CI contains a group invocation outside ci/run-ci.sh --list"
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
# shellcheck disable=SC2016 # Match the literal GitHub expression.
rg -Fq 'browser_readiness_source_sha: ${{ needs.prepare.outputs.head_sha }}' <<<"$preview_deploy_job" ||
  fail "preview deploy does not bind the isolated browser script to the resolved PR-head commit"
preview_destroy_job="$(sed -n '/^  destroy:/,/^  record-preview-deployment:/p' .github/workflows/terraform-preview.yaml)"
if rg -q 'browser_readiness_source_sha' <<<"$preview_destroy_job"; then
  fail "preview destroy must replay the recorded digest without fetching PR-head source"
fi
for preview_deploy_job in \
  "$preview_deploy_job" \
  "$preview_destroy_job"; do
  rg -q '^      packages: read$' <<<"$preview_deploy_job" ||
    fail "preview deploy and destroy callers must satisfy the reusable workflow package-read contract"
done

browser_source_line="$(rg -n 'name: Stage exact PR-head browser readiness source' .github/workflows/terraform-deploy.yaml | cut -d: -f1)"
cloud_auth_line="$(rg -n 'name: Authenticate to Google Cloud' .github/workflows/terraform-deploy.yaml | cut -d: -f1)"
[[ "$browser_source_line" =~ ^[0-9]+$ && "$cloud_auth_line" =~ ^[0-9]+$ && "$browser_source_line" -lt "$cloud_auth_line" ]] ||
  fail "the exact PR-head browser source must be validated before cloud credentials exist"

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
