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
)

cd "${ROOT_DIR}"

refuse_tracked_runtime_secrets() {
  local path
  local -a rejected=()

  git rev-parse --is-inside-work-tree >/dev/null 2>&1 || {
    echo "Error: dependency scanning requires a Git worktree." >&2
    return 1
  }
  while IFS= read -r -d '' path; do
    case "$path" in
      .env | .env/* | */.env | */.env/* | \
        gha-creds-*.json | gha-creds-*.json/* | \
        */gha-creds-*.json | */gha-creds-*.json/* | \
        secrets | secrets/* | */secrets | */secrets/*)
        rejected+=("$path")
        ;;
    esac
  done < <(git ls-files -z)
  if ((${#rejected[@]} == 0)); then
    return 0
  fi

  echo "Refusing dependency scan: Git tracks runtime-secret paths excluded from the sanitized scan root:" >&2
  printf '  %q\n' "${rejected[@]}" >&2
  return 1
}

host_trivy_is_pinned() {
  local actual
  actual="$(trivy --version 2>/dev/null | awk -F': ' '$1 == "Version" { print $2; exit }')"
  [ "$actual" = "$EXPECTED_TRIVY_VERSION" ]
}

refuse_tracked_runtime_secrets

SCAN_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/scribe-dependency-scan.XXXXXX")"
container_id=""
cleanup() {
  if [ -n "${container_id}" ]; then
    docker rm -fv "${container_id}" >/dev/null 2>&1 || true
  fi
  rm -rf -- "${SCAN_ROOT}"
}
trap cleanup EXIT

# Both the host and container scanners consume this same snapshot. Untracked
# runtime credentials stay outside it; the preflight above makes their paths
# fail closed if they are ever force-added to Git.
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
  -C "${ROOT_DIR}" -cf - . | tar -C "${SCAN_ROOT}" -xf -

if command -v trivy >/dev/null 2>&1 && host_trivy_is_pinned; then
  trivy "${TRIVY_ARGS[@]}" "${SCAN_ROOT}"
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
    "${TRIVY_ARGS[@]}" .
)"

tar -C "${SCAN_ROOT}" -cf - . | docker cp - "${container_id}:/repo"

docker start -a "${container_id}"
