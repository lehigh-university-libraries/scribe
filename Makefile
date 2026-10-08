.PHONY: help
.PHONY: build build-frontend frontend-image-smoke vault-init-image-smoke fmt fmt-check lint toolchain-check check test test-backend test-backend-fast test-frontend test-browser e2e-smoke backup-restore-smoke readiness-fixture-test ocr-build-tags segmentor-lock segmentor-lock-check export-schema-check proto proto-lint sqlc generate generate-check security dependency-scan ops-tests terraform-check docs docs-build docs-serve install-tools install-shell-tools install-codegen-tools install-security-tools install-doc-tools doctor ci up up-cloud-ocr up-db reset-dev-db down logs sequelace ocr-matrix bootstrap-gcp-identities tf-dev tf-prod tf-preview vault-secrets

IMAGE ?= scribe-api:local
FRONTEND_IMAGE ?= scribe-frontend:local
COMPOSE_UP_FLAGS ?= -d
REBUILD ?= false
# renovate: datasource=docker depName=golangci/golangci-lint
GOLANGCI_IMAGE ?= golangci/golangci-lint:v2.12.2-alpine@sha256:91b27804074a0bacea298707f016911e60cf0cdbc6c7bf5ccacb5f0606d18d60
TOOLS_BIN ?= $(CURDIR)/.tools/bin
# Terraform and gcloud targets use the caller's PATH so pinned dev tools never
# shadow the operator's own Terraform.
HOST_PATH := $(PATH)
export PATH := $(TOOLS_BIN):$(PATH)
GO_CMD ?= $(shell command -v go 2>/dev/null || { test -x /usr/local/go/bin/go && printf '%s' /usr/local/go/bin/go; })

help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Available targets:'
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the backend Docker image for the API and worker
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

ocr-matrix: ## Print the OCR image build matrix from config/ocr.yaml. Usage: GCLOUD_PROJECT=... make ocr-matrix [TAG=main]
	@go run ./cmd/ocr-matrix -tag "$(or $(TAG),main)"

bootstrap-gcp-identities: ## One-time setup of CI identities, state bucket retention, and the alert channel. Usage: GCLOUD_PROJECT=... MONITORING_NOTIFICATION_EMAIL=... make bootstrap-gcp-identities
	@./scripts/bootstrap-gcp-identities.sh

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

security: ## Run gosec and npm audits (opt in to govulncheck with SCRIBE_GOVULNCHECK=true)
	@./ci/security.sh

dependency-scan: ## Scan locked dependencies for fixed high and critical vulnerabilities
	@./ci/dependency-scan.sh

ops-tests: install-shell-tools ## Exercise local runtime, identity, secret, and recovery scripts
	@bash ./ci/ensure-local-vault-init-image_test.sh
	@bash ./ci/cloud-ocr-compose-preflight_test.sh
	@bash ./ci/run-ci-network_test.sh
	@bash ./ci/configure-dev-cloud-ocr_test.sh
	@bash ./ci/tool-version_test.sh
	@bash ./ci/toolchain-check_test.sh
	@bash ./ci/update-env_test.sh
	@bash ./ci/persistence-generation_test.sh
	@bash ./ci/compose-network-ipam_test.sh
	@bash ./ci/ocr-local-defaults_test.sh
	@bash ./ci/vault-database-path_test.sh
	@bash ./ci/generate-secrets-permissions_test.sh
	@bash ./ci/vault-init-image_test.sh
	@bash ./ci/vault-init-diagnostics_test.sh
	@bash ./ci/vault-policy-capabilities_test.sh
	@bash ./ci/vault-retry_test.sh
	@bash ./ci/npm-audit_test.sh
	@bash ./ci/dependency-scan-path-parity_test.sh
	@node --test ./web/e2e/editor-dom.test.mjs

terraform-check: ## Format-check, initialize, and validate Terraform
	@./ci/terraform-check.sh

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

.PHONY: pdf-export-smoke
pdf-export-smoke: ## Verify the packaged Scyllaridae PDF service preserves corrected text and images
	@bash ./ci/pdf-export-smoke.sh

test-browser: ## Run real Chromium editor acceptance tests in the pinned Playwright container
	@bash ./ci/test-browser.sh

e2e-smoke: ## Run the containerized DB-backed ingest/edit/save/reload smoke path
	@bash ./ci/e2e-smoke.sh

backup-restore-smoke: ## Exercise isolated MySQL/blob backup, restore, integrity, and expired-job recovery
	@bash ./ci/backup-restore-smoke.sh

test-mysql: ## Run migrations, stores, leases, fencing, and outboxes against pinned MySQL 8.4
	@bash ./ci/test-mysql.sh

readiness-fixture-test: ## Verify the deterministic non-empty OCR deployment smoke fixture and assertions
	@bash ./ci/readiness-fixture-test.sh

ocr-build-tags: ## Build and test the default, remoteocr, and localocr modes
	@bash ./ci/ocr-build-tags.sh

# Apply the named Terraform workspace directly.
define terraform
@set -eu; \
	export PATH="$(HOST_PATH)"; \
	: "$${GCLOUD_PROJECT:?set GCLOUD_PROJECT}"; \
	export TF_VAR_project_id="$$GCLOUD_PROJECT"; \
	action="$(or $(ACTION),plan)"; \
	terraform -chdir=terraform init -input=false -lockfile=readonly \
		-backend-config="bucket=$${TF_STATE_BUCKET:-$$GCLOUD_PROJECT-terraform}" -backend-config="prefix=scribe"; \
	terraform -chdir=terraform workspace select -or-create "$(1)"; \
	terraform -chdir=terraform "$$action" $(ARGS)
endef

tf-dev: ## Terraform for the shared dev environment. Usage: make tf-dev [ACTION=plan|apply|destroy] [ARGS=...]
	$(call terraform,dev)

tf-prod: ## Terraform for production. Usage: make tf-prod [ACTION=plan|apply|destroy] [ARGS=...]
	$(call terraform,prod)

tf-preview: ## Terraform for a PR preview. Usage: make tf-preview PR=23 [ACTION=plan|apply|destroy] [ARGS=...]
	@test -n "$(PR)" || { echo "set PR=<number>" >&2; exit 1; }
	$(call terraform,pr-$(PR))

vault-secrets: ## Set the application secrets in Vault. Usage: make vault-secrets [WORKSPACE=dev|prod] [CMD=update|show]
	@set -eu; \
	export PATH="$(HOST_PATH)"; \
	: "$${GCLOUD_PROJECT:?set GCLOUD_PROJECT}"; \
	ws="$(or $(WORKSPACE),dev)"; \
	VAULT_ADDR="$$(gcloud run services describe "vault-server-$$ws" --project "$$GCLOUD_PROJECT" --region "$${TF_VAR_region:-us-east5}" --format='value(status.url)')" \
	VAULT_TOKEN="$${VAULT_TOKEN:-$$(./scripts/vault-token.sh "$$ws")}" \
	VAULT_ADMIN_TOKEN="$$(gcloud auth print-access-token)" \
		go run ./cmd/vault-secrets -workspace "$$ws" $(or $(CMD),update)

.PHONY: secret-manager-secrets
secret-manager-secrets: ## Copy and verify Vault bootstrap secrets in Secret Manager. Usage: make secret-manager-secrets WORKSPACE=dev|prod
	@set -eu; \
	 export PATH="$(HOST_PATH)"; \
	 : "$${GCLOUD_PROJECT:?set GCLOUD_PROJECT}"; \
	 workspace="$(or $(WORKSPACE),dev)"; \
	 case "$$workspace" in dev|prod) ;; *) echo 'WORKSPACE must be dev or prod' >&2; exit 2 ;; esac; \
	 service="vault-server-$$workspace"; \
	 export VAULT_ADDR="$${VAULT_ADDR:-$$(gcloud run services describe "$$service" --project "$$GCLOUD_PROJECT" --region "$${TF_VAR_region:-us-east5}" --format='value(status.url)')}"; \
	 export VAULT_TOKEN="$${VAULT_TOKEN:-$$(./scripts/vault-token.sh "$$workspace")}"; \
	 export VAULT_ADMIN_TOKEN="$${VAULT_ADMIN_TOKEN:-$$(gcloud auth print-access-token)}"; \
	 go run ./cmd/secret-manager-secrets -workspace "$$workspace" -project "$$GCLOUD_PROJECT"

.PHONY: test-mysql triplet-sql-test
triplet-sql-test: ## Verify shared Triplet SQL CAS and restart persistence
	@bash ./ci/triplet-sql_test.sh
