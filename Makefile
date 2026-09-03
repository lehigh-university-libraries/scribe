.PHONY: help
.PHONY: build build-frontend frontend-image-smoke vault-init-image-smoke fmt fmt-check lint toolchain-check check test test-backend test-backend-fast test-frontend test-browser e2e-smoke backup-restore-smoke verify-cloud-backups-test cloud-snapshot-restore-drill-test mariadb-backup-retention-test preview-deployment-test readiness-fixture-test deployment-status-test reset-dev-db-test ocr-build-tags ocr-matrix-test ocr-source-paths-test segmentor-lock segmentor-lock-check export-schema-check proto proto-lint sqlc generate generate-check security dependency-scan ops-tests terraform-check terraform-state-normalizer-test terraform-targeted-output-test docs docs-build docs-serve install-tools install-shell-tools install-codegen-tools install-security-tools install-doc-tools doctor ci up up-cloud-ocr up-db reset-dev-db down logs sequelace ocr-matrix ocr-images bootstrap-gcp-identities bootstrap-gcp-identities-test tf-dev tf-dev-vault-ci-identities tf-dev-vault-preview-runtime tf-dev-ocr tf-prod tf-prod-ocr tf-preview vault-secrets

IMAGE ?= ghcr.io/lehigh-university-libraries/scribe:main
FRONTEND_IMAGE ?= scribe-frontend:local
COMPOSE_UP_FLAGS ?= -d
REBUILD ?= false
# renovate: datasource=docker depName=golangci/golangci-lint
GOLANGCI_IMAGE ?= golangci/golangci-lint:v2.13.2-alpine@sha256:da1f79b184ff1d98781648407d05a99b9cba7b282aa3416b0a0eac102a3b0557
TOOLS_BIN ?= $(CURDIR)/.tools/bin
export PATH := $(TOOLS_BIN):$(PATH)
GO_CMD ?= $(shell command -v go 2>/dev/null || { test -x /usr/local/go/bin/go && printf '%s' /usr/local/go/bin/go; })

help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Available targets:'
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the backend Docker image used on the VM
	@IMAGE="$(IMAGE)" ./ci/build.sh

build-frontend: ## Build the frontend Docker image
	@FRONTEND_IMAGE="$(FRONTEND_IMAGE)" ./ci/build-frontend.sh

frontend-image-smoke: ## Start the packaged frontend image and verify its runtime module graph
	@./ci/frontend-image-smoke.sh "$(FRONTEND_IMAGE)"

vault-init-image-smoke: ## Verify the packaged backend image contains the immutable Vault init helper
	@./ci/vault-init-image-smoke.sh "$(IMAGE)"

doctor: ## Check the local Docker/Git toolchain and report optional host runtimes
	@./ci/doctor.sh

up: doctor ## Start services in detached mode; set REBUILD=true to rebuild images
	@test -f .env || cp sample.env .env
	@test -f docker-compose.override.yaml || cp docker-compose.override-example.yaml docker-compose.override.yaml
	@REBUILD="$(REBUILD)" ./ci/ensure-local-vault-init-image.sh
	@SCRIBE_REPAIR_LOCAL_TOKENS=true bash generate-secrets.sh
	@docker compose up $(COMPOSE_UP_FLAGS) $(if $(filter true,$(REBUILD)),--build,)

up-cloud-ocr: doctor ## Start local services against configured private Cloud Run OCR endpoints; set REBUILD=true to rebuild
	@test -f .env || cp sample.env .env
	@test -f docker-compose.override.yaml || cp docker-compose.override.cloud-example.yaml docker-compose.override.yaml
	@cloud_ocr_project="$$(bash ./ci/cloud-ocr-compose-preflight.sh --print-project)" && \
		bash ./ci/validate-dev-cloud-ocr-credential.sh \
			secrets/GOOGLE_APPLICATION_CREDENTIALS "$$cloud_ocr_project"
	@REBUILD="$(REBUILD)" ./ci/ensure-local-vault-init-image.sh
	@SCRIBE_REPAIR_LOCAL_TOKENS=true bash generate-secrets.sh
	@docker compose up $(COMPOSE_UP_FLAGS) $(if $(filter true,$(REBUILD)),--build,)

up-db: ## Start only MariaDB for DB-backed integration tests
	@test -f .env || cp sample.env .env
	@REBUILD="$(REBUILD)" ./ci/ensure-local-vault-init-image.sh
	@bash generate-secrets.sh
	@docker compose up -d --wait --wait-timeout 120 mariadb

reset-dev-db: ## Delete only the local Compose MariaDB data (explicit confirmation required)
	@bash ./ci/reset-dev-db.sh

down: ## Stop compose services and remove orphans
	@docker compose down --remove-orphans

logs: ## Follow logs for the API
	@docker compose logs api --tail 20 -f

sequelace: ## Open the local MariaDB in Sequel Ace (macOS)
	@./ci/sequelace.sh

ocr-matrix: install-shell-tools ## Print the OCR build matrix derived from config/ocr.yaml. Usage: GCLOUD_PROJECT=... WORKSPACE_SLUG=prod IMAGE_TAG=<commit> make ocr-matrix
	@./ci/ocr-matrix.sh

ocr-images: install-shell-tools ## Resolve/build GAR OCR images from config/ocr.yaml. Usage: GCLOUD_PROJECT=... WORKSPACE_SLUG=prod IMAGE_TAG=<commit> [AUTO_BUILD_MISSING=true] make ocr-images
	@./ci/generate-ocr-images-map.sh

bootstrap-gcp-identities: ## Plan/apply external WIF, state-bucket, and notification resources. Usage: make bootstrap-gcp-identities [ACTION=plan|apply]
	@action="$(ACTION)"; \
	if [ -z "$$action" ]; then action="plan"; fi; \
	case "$$action" in plan|apply) ;; *) echo "ACTION must be plan or apply" >&2; exit 1 ;; esac; \
	./ci/bootstrap-external-gcp-identities.sh "$$action"

bootstrap-gcp-identities-test: ## Exercise the external GCP identity bootstrap with a stateful fake gcloud
	@bash ./ci/bootstrap-external-gcp-identities_test.sh

segmentor-lock: ## Regenerate the hash-locked Python dependency graph used by Dockerfile.segmentor
	@./ci/segmentor-lock.sh

segmentor-lock-check: ## Verify the committed Segmentor Python lock and Docker enforcement
	@./ci/segmentor-lock-check.sh

fmt: ## Format changed Go files
	@./ci/fmt.sh

fmt-check: install-shell-tools ## Fail if tracked Go source is not gofmt-formatted
	@./ci/fmt-check.sh

lint: install-shell-tools toolchain-check fmt-check proto-lint ## Lint shell, Go, and protobuf source
	@IMAGE="$(IMAGE)" GOLANGCI_IMAGE="$(GOLANGCI_IMAGE)" ./ci/lint.sh

proto: ## Generate protobuf/connect code
	@./ci/proto.sh

proto-lint: ## Lint protobuf files
	@./ci/proto-lint.sh

sqlc: ## Generate SQL access code
	@./ci/sqlc.sh

generate: proto sqlc ## Generate all code (proto + sqlc)
	@echo "✅ All code generation complete!"

generate-check: segmentor-lock-check ## Regenerate contracts and fail if committed output is stale
	@./ci/generate-check.sh

install-tools: install-shell-tools install-codegen-tools install-security-tools ## Install all pinned developer tools under .tools/bin

install-shell-tools: ## Install checksum-pinned shell tools under .tools/bin
	@TOOLS_BIN="$(TOOLS_BIN)" ./ci/install-ripgrep.sh
	@TOOLS_BIN="$(TOOLS_BIN)" ./ci/install-yq.sh

install-codegen-tools: ## Install pinned Buf and sqlc under .tools/bin
	@./ci/toolchain-check.sh --go
	@mkdir -p "$(TOOLS_BIN)"
	@echo "Installing pinned generators in $(TOOLS_BIN)..."
	@GOBIN="$(TOOLS_BIN)" "$(GO_CMD)" install github.com/bufbuild/buf/cmd/buf@v1.72.0
	@GOBIN="$(TOOLS_BIN)" "$(GO_CMD)" install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1

install-security-tools: ## Install pinned gosec and govulncheck under .tools/bin
	@./ci/toolchain-check.sh --go
	@mkdir -p "$(TOOLS_BIN)"
	@echo "Installing pinned security scanners in $(TOOLS_BIN)..."
	@GOBIN="$(TOOLS_BIN)" "$(GO_CMD)" install github.com/securego/gosec/v2/cmd/gosec@v2.28.0
	@GOBIN="$(TOOLS_BIN)" "$(GO_CMD)" install golang.org/x/vuln/cmd/govulncheck@v1.6.0

install-doc-tools: ## Build the pinned local Zensical documentation image
	@command -v docker >/dev/null 2>&1 || { echo "Docker is required" >&2; exit 127; }
	@docker build -f Dockerfile.docs -t scribe-docs:local .

docs-build: install-doc-tools ## Build the strict Zensical documentation site into ./site
	@SCRIBE_DOCS_FORCE_DOCKER=true ./ci/docs.sh build

docs: docs-build ## Alias for docs-build

docs-serve: install-doc-tools ## Serve docs locally with live reload
	@SCRIBE_DOCS_FORCE_DOCKER=true ./ci/docs.sh serve

toolchain-check: ## Verify version files, containers, scripts, and workflows remain aligned
	@./ci/toolchain-check.sh

security: ## Run Go reachability/static scans and npm advisory gates
	@./ci/security.sh

dependency-scan: ## Scan locked dependencies for fixed high and critical vulnerabilities
	@./ci/dependency-scan.sh

reset-dev-db-test: ## Verify the local MariaDB reset refuses broad or unconfirmed deletion
	@bash ./ci/reset-dev-db_test.sh

ops-tests: install-shell-tools reset-dev-db-test ## Exercise deployment, identity, secret, and recovery behavior
	@bash ./ci/ensure-local-vault-init-image_test.sh
	@bash ./ci/prepare-browser-readiness-source_test.sh
	@bash ./ci/deployed-readiness-entrypoint_test.sh
	@node --check ./web/e2e/deployed-readiness.mjs
	@node --test ./web/e2e/deployed-readiness-budget.test.mjs
	@node --test ./web/e2e/deployed-readiness-dom.test.mjs
	@node --test ./web/e2e/deployed-readiness-structure.test.mjs
	@node --test ./web/e2e/deployed-readiness-routing_test.mjs
	@bash ./ci/run-production-browser-readiness_test.sh
	@bash ./ci/cloud-ocr-compose-preflight_test.sh
	@bash ./ci/run-ci-network_test.sh
	@bash ./ci/configure-dev-cloud-ocr_test.sh
	@bash ./ci/dev-external-ocr-iam_test.sh
	@bash ./ci/tool-version_test.sh
	@bash ./ci/toolchain-check_test.sh
	@bash ./ci/update-env_test.sh
	@bash ./ci/verify-gcp-wif_test.sh
	@bash ./ci/bootstrap-external-gcp-identities_test.sh
	@bash ./ci/capture-command-log_test.sh
	@bash ./ci/gcp-vm-bootstrap-diagnostics_test.sh
	@bash ./ci/run-cloud-run-readiness_test.sh
	@bash ./ci/persistence-generation_test.sh
	@bash ./ci/compose-network-ipam_test.sh
	@bash ./ci/compose-runtime-preflight_test.sh
	@bash ./ci/resolve-rollback-inputs_test.sh
	@bash ./ci/select-current-ocr-images_test.sh
	@bash ./ci/verify-production-source-lineage_test.sh
	@bash ./ci/verify-production-persistent-disk-plan_test.sh
	@bash ./ci/vault-database-path_test.sh
	@bash ./ci/generate-secrets-permissions_test.sh
	@bash ./ci/vault-init-image_test.sh
	@bash ./ci/vault-init-diagnostics_test.sh
	@bash ./ci/vault-first-bootstrap_test.sh
	@bash ./ci/preview-vault-secrets_test.sh
	@bash ./ci/vault-policy-capabilities_test.sh
	@bash ./ci/vault-retry_test.sh
	@bash ./ci/vault-terraform-readiness_test.sh
	@bash ./ci/publish-preview-images_test.sh
	@bash ./ci/resolve-gar-image_test.sh
	@bash ./ci/dependency-scan-path-parity_test.sh
	@bash ./ci/release-tag_test.sh
	@bash ./ci/release-draft_test.sh
	@bash ./ci/release-coverage_test.sh
	@bash ./ci/verify-release-source-run_test.sh

terraform-check: ## Format-check, initialize, and validate Terraform
	@./ci/terraform-check.sh

terraform-state-normalizer-test: ## Exercise transitive moved-state normalization with an isolated local Terraform state
	@bash ./ci/normalize-terraform-moved-state_test.sh

terraform-targeted-output-test: ## Prove targeted maintenance cannot rewrite recorded deployment outputs
	@sh ./ci/terraform-targeted-output_test.sh

ci: ## Run every required CI gate with an isolated, automatically removed integration database
	@SCRIBE_MAKE_COMMAND="$(MAKE)" bash ./ci/run-ci.sh

check: ## Run the fast local pre-push checks serially; make ci remains the release contract
	@$(MAKE) lint
	@$(MAKE) generate-check
	@$(MAKE) test-backend-fast

test: ## Run frontend checks and Go tests (integration tests run automatically if MariaDB is active via make up-db or make up)
	@$(MAKE) test-frontend
	@$(MAKE) test-backend

test-backend: ## Run Go tests (integration tests run automatically if MariaDB is active via make up-db or make up)
	@./ci/test.sh

test-backend-fast: ## Run cached, parallel Go unit tests with the pinned host toolchain when available
	@./ci/test.sh --fast

export-schema-check: ## Validate PAGE and ALTO export fixtures against pinned official schemas
	@./ci/export-schema-check.sh

test-frontend: ## Run frontend tests and production build checks
	@bash ./ci/test-frontend.sh

test-browser: ## Run real Chromium editor acceptance tests in the pinned Playwright container
	@bash ./ci/test-browser.sh

e2e-smoke: ## Run the containerized DB-backed ingest/edit/save/reload smoke path
	@bash ./ci/e2e-smoke.sh

backup-restore-smoke: ## Exercise isolated MariaDB/blob backup, restore, integrity, and expired-job recovery
	@bash ./ci/backup-restore-smoke.sh

verify-cloud-backups-test: ## Test production cloud-backup policy and freshness verification
	@bash ./ci/verify-cloud-backups_test.sh

cloud-snapshot-restore-drill-test: ## Test isolated GCE snapshot restore selection, probing, and cleanup with a fake gcloud
	@bash ./ci/cloud-snapshot-restore-drill_test.sh

mariadb-backup-retention-test: ## Verify logical-backup retention is bounded and fails closed on unsafe entries
	@bash ./ci/mariadb-backup-retention_test.sh

preview-deployment-test: install-shell-tools ## Test trusted preview input resolution and immutable teardown
	@bash ./ci/resolve-preview-inputs_test.sh
	@bash ./ci/recover-preview-destroy-inputs_test.sh
	@bash ./ci/deploy-local-destroy_test.sh

readiness-fixture-test: ## Verify the deterministic non-empty OCR deployment smoke fixture and assertions
	@bash ./ci/readiness-fixture-test.sh

deployment-status-test: ## Exercise plan/apply/readiness status precedence
	@bash ./ci/deployment-status_test.sh

ocr-build-tags: ocr-matrix-test ## Validate the OCR model matrix, then build/test default, remoteocr, and localocr modes
	@bash ./ci/ocr-build-tags.sh

ocr-matrix-test: install-shell-tools ocr-source-paths-test ## Verify OCR model configuration, routes, and build matrix generation
	@bash ./ci/ocr-matrix_test.sh

ocr-source-paths-test: ## Verify OCR source discovery covers the transitive segmentor build graph
	@bash ./ci/ocr-source-paths_test.sh

define run_terraform_local
@set -eu; \
	environment="$(1)"; \
	action="$(ACTION)"; \
	if [ -z "$$action" ]; then action="plan"; fi; \
	case "$$environment" in dev|prod|preview) ;; *) echo "invalid Terraform environment: $$environment" >&2; exit 2 ;; esac; \
	set -- "$$environment" "$$action"; \
	if [ -n "$(BRANCH)" ]; then set -- "$$@" --branch "$(BRANCH)"; fi; \
	if [ "$$environment" = "preview" ]; then \
		pr="$(PR)"; \
		if [ -z "$$pr" ]; then echo "set PR=<number>" >&2; exit 1; fi; \
		set -- "$$@" --pr-number "$$pr"; \
	fi; \
	TF_TARGET_SET="$(2)" ./terraform/deploy-local.sh "$$@"
endef

tf-dev: ## Run local Terraform for the shared dev environment. Usage: make tf-dev [BRANCH=name] ACTION=plan|apply|refresh|normalize-moves|destroy
	$(call run_terraform_local,dev,)

tf-dev-vault-ci-identities: ## Reconcile only shared dev Vault CI login identities. Usage: make tf-dev-vault-ci-identities [BRANCH=name] ACTION=plan|apply
	$(call run_terraform_local,dev,vault-ci-identities)

tf-dev-vault-preview-runtime: ## Check or reconcile the shared dev preview runtime Vault policy and role. Usage: make tf-dev-vault-preview-runtime [BRANCH=name] ACTION=plan|apply
	$(call run_terraform_local,dev,vault-preview-runtime)

tf-dev-ocr: ## Reapply only the shared dev OCR helper services. Usage: make tf-dev-ocr [BRANCH=name] ACTION=plan|apply
	$(call run_terraform_local,dev,ocr)

tf-prod: ## Run local Terraform for production. Usage: make tf-prod [BRANCH=<40-character-sha>] ACTION=plan|apply|refresh|normalize-moves|destroy
	$(call run_terraform_local,prod,)

tf-prod-ocr: ## Reapply only production OCR helper services, including Ollama Cloud Run. Usage: make tf-prod-ocr BRANCH=<40-character-sha> ACTION=plan|apply
	$(call run_terraform_local,prod,ocr)

tf-preview: ## Run local Terraform for a preview env. Usage: make tf-preview PR=23 [BRANCH=<40-character-base-sha>] ACTION=plan|apply|refresh|normalize-moves|destroy
	$(call run_terraform_local,preview,)

vault-secrets: ## Interactively manage Vault secrets for dev or prod
	@./ci/vault-secrets.sh
