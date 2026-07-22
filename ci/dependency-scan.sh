#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly EXPECTED_TRIVY_VERSION="0.69.3"
# Keep this version aligned with aquasecurity/trivy-action in lint-test.yaml.
# renovate: datasource=docker depName=aquasec/trivy
TRIVY_IMAGE="${TRIVY_IMAGE:-aquasec/trivy:0.69.3@sha256:bcc376de8d77cfe086a917230e818dc9f8528e3c852f7b1aff648949b6258d1c}"
TRIVY_ARGS=(
  fs
  --scanners "vuln,secret"
  --severity "HIGH,CRITICAL"
  --ignore-unfixed
  --exit-code 1
  --skip-dirs .git
  --skip-dirs .tools
  --skip-dirs node_modules
  --skip-dirs site
  --skip-dirs terraform/.terraform
  .
)

cd "${ROOT_DIR}"

host_trivy_is_pinned() {
  local actual
  actual="$(trivy --version 2>/dev/null | awk -F': ' '$1 == "Version" { print $2; exit }')"
  [ "$actual" = "$EXPECTED_TRIVY_VERSION" ]
}

if command -v trivy >/dev/null 2>&1 && host_trivy_is_pinned; then
  trivy "${TRIVY_ARGS[@]}"
  exit 0
fi

if command -v trivy >/dev/null 2>&1; then
  echo "Installed Trivy is not ${EXPECTED_TRIVY_VERSION}; using the pinned container." >&2
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "Error: Trivy ${EXPECTED_TRIVY_VERSION} or Docker is required." >&2
  exit 127
fi

container_id="$(
  docker create \
    --workdir /repo \
    --entrypoint trivy \
    "${TRIVY_IMAGE}" \
    "${TRIVY_ARGS[@]}"
)"
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
  --exclude='web/node_modules*' \
  --exclude=web/dist \
  --exclude=web/test-results \
  --exclude=web/playwright-report \
  --exclude='mirador-scribe/node_modules*' \
  --exclude=mirador-scribe/dist \
  -C "${ROOT_DIR}" -cf - . | docker cp - "${container_id}:/repo"

docker start -a "${container_id}"
