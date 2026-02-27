.PHONY: help
.PHONY: build fmt lint test proto proto-lint sqlc generate install-tools up

IMAGE ?= ghcr.io/lehigh-university-libraries/hocredit:main
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
	@docker compose up -d

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
	@go install github.com/bufbuild/buf/cmd/buf@v1.61.0
	@go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
	@go install connectrpc.com/connect/cmd/protoc-gen-connect-go@v1.19.1
	@go install github.com/sudorandom/protoc-gen-connect-openapi@v0.21.3
	@go install github.com/google/gnostic/cmd/protoc-gen-openapi@v0.7.0
	@go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0

test: ## Run Go tests
	@./ci/test.sh
