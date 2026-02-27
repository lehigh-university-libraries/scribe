.PHONY: help
.PHONY: build fmt lint test

IMAGE ?= ghcr.io/lehigh-university-libraries/hocredit:main
# renovate: datasource=docker depName=golangci/golangci-lint
GOLANGCI_IMAGE ?= golangci/golangci-lint:v2.10.1-alpine

help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Available targets:'
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the Docker image used for linting/runtime
	@IMAGE="$(IMAGE)" ./ci/build.sh

fmt: ## Format all go code the CLI
	@./ci/fmt.sh

lint: build ## Lint Go code
	@IMAGE="$(IMAGE)" GOLANGCI_IMAGE="$(GOLANGCI_IMAGE)" ./ci/lint.sh

test: build ## Run all tests
	@IMAGE="$(IMAGE)" ./ci/test.sh
