#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

REQUIRED_TERRAFORM_VERSION="$(awk '$1 == "terraform" { print $2 }' .tool-versions)"
# renovate: datasource=docker depName=hashicorp/terraform
TERRAFORM_IMAGE="${TERRAFORM_IMAGE:-hashicorp/terraform:1.15.8@sha256:7ae513256f7ce67879e218ae8593d6fbe216ec9e123abe6c94e4e10704857963}"

run_checks() {
  terraform fmt -check -recursive terraform
  terraform -chdir=terraform init -backend=false -input=false -lockfile=readonly
  terraform -chdir=terraform/foundation init -backend=false -input=false -lockfile=readonly
  sh ci/terraform-moved-refresh_test.sh
  terraform -chdir=terraform validate
  terraform -chdir=terraform/foundation validate
}

if command -v terraform >/dev/null 2>&1; then
  installed_version="$(terraform version -json | jq -r '.terraform_version')"
  if [ "${installed_version}" = "${REQUIRED_TERRAFORM_VERSION}" ]; then
    run_checks
    exit 0
  fi
  echo "Terraform ${installed_version} is installed; using pinned ${REQUIRED_TERRAFORM_VERSION} in Docker." >&2
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "Error: Terraform ${REQUIRED_TERRAFORM_VERSION} or Docker is required." >&2
  exit 127
fi

check_command='terraform fmt -check -recursive terraform && terraform -chdir=terraform init -backend=false -input=false -lockfile=readonly && terraform -chdir=terraform/foundation init -backend=false -input=false -lockfile=readonly && sh ci/terraform-moved-refresh_test.sh && terraform -chdir=terraform validate && terraform -chdir=terraform/foundation validate'
container_id="$(
  docker create \
    --workdir /repo \
    --entrypoint sh \
    "${TERRAFORM_IMAGE}" \
    -c "${check_command}"
)"
# Invoked by the EXIT trap below.
# shellcheck disable=SC2329
cleanup() {
  docker rm -fv "${container_id}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

tar \
  --exclude=.env \
  --exclude=.git \
  --exclude=.tools \
  --exclude='gha-creds-*.json' \
  --exclude=secrets \
  --exclude=site \
  --exclude=terraform/.terraform \
  --exclude=terraform/foundation/.terraform \
  --exclude='web/node_modules*' \
  --exclude=web/dist \
  --exclude='mirador-scribe/node_modules*' \
  --exclude=mirador-scribe/dist \
  -C "${ROOT_DIR}" -cf - . | docker cp - "${container_id}:/repo"
docker start -a "${container_id}"
