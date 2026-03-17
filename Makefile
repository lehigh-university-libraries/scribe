.PHONY: help
.PHONY: build fmt lint test proto proto-lint sqlc generate install-tools up logs sequelace tf-prod tf-preview

IMAGE ?= ghcr.io/lehigh-university-libraries/scribe:main
COMPOSE_UP_FLAGS ?= -d --build
# renovate: datasource=docker depName=golangci/golangci-lint
GOLANGCI_IMAGE ?= golangci/golangci-lint:v2.10.1-alpine

help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Available targets:'
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the Docker image used for runtime
	@IMAGE="$(IMAGE)" ./ci/build.sh

up: ## Start services in detached mode
	@test -f .env || cp sample.env .env
	@bash generate-secrets.sh
	@docker compose up $(COMPOSE_UP_FLAGS)

logs: ## Follow logs for the API
	@docker compose logs api --tail 20 -f

sequelace: ## Open the local MariaDB in Sequel Ace (macOS)
	@./ci/sequelace.sh

fmt: ## Format changed Go files
	@./ci/fmt.sh

lint: ## Lint shell + Go + optional proto
	@IMAGE="$(IMAGE)" GOLANGCI_IMAGE="$(GOLANGCI_IMAGE)" ./ci/lint.sh

proto: ## Generate protobuf/connect code
	@./ci/proto.sh

proto-lint: ## Lint protobuf files
	@./ci/proto-lint.sh

sqlc: ## Generate SQL access code
	@./ci/sqlc.sh

generate: proto sqlc ## Generate all code (proto + sqlc)
	@echo "✅ All code generation complete!"

install-tools: ## Install required development tools
	@echo "Installing development tools..."
	@go install github.com/bufbuild/buf/cmd/buf@v1.66.1
	@go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
	@go install connectrpc.com/connect/cmd/protoc-gen-connect-go@v1.19.1
	@go install github.com/sudorandom/protoc-gen-connect-openapi@v0.21.3
	@go install github.com/google/gnostic/cmd/protoc-gen-openapi@v0.7.0
	@go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0

test: ## Run Go tests (integration tests run automatically if 'make up' is active)
	@./ci/test.sh

tf-prod: ## Run local Terraform for production. Usage: make tf-prod ACTION=plan|apply|destroy
	@set -eu; \
	action="${ACTION}"; \
	if [ -z "$$action" ]; then action="plan"; fi; \
	./terraform/deploy-local.sh prod "$$action"

tf-preview: ## Run local Terraform for a preview env. Usage: make tf-preview PR=23 [BRANCH=name] ACTION=plan|apply|destroy
	@set -eu; \
	action="${ACTION}"; \
	if [ -z "$$action" ]; then action="plan"; fi; \
	pr="${PR}"; \
	if [ -z "$$pr" ]; then echo "set PR=<number>" >&2; exit 1; fi; \
	branch_arg=""; \
	if [ -n "${BRANCH}" ]; then branch_arg="--branch ${BRANCH}"; fi; \
	./terraform/deploy-local.sh preview "$$action" $$branch_arg --pr-number "$$pr"
