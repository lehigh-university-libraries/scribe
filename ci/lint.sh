#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

readonly EXPECTED_SHELLCHECK_VERSION="0.11.0"
readonly EXPECTED_ACTIONLINT_VERSION="1.7.12"
readonly EXPECTED_GOLANGCI_LINT_VERSION="2.13.2"
readonly ACTIONLINT_CONFIG=".github/actionlint.yaml"

host_shellcheck_is_pinned() {
  local actual
  actual="$(shellcheck --version 2>/dev/null | awk -F': ' '$1 == "version" { print $2; exit }')"
  [ "$actual" = "$EXPECTED_SHELLCHECK_VERSION" ]
}

host_actionlint_is_pinned() {
  local actual
  actual="$(actionlint --version 2>/dev/null | sed -n '1s/^v//p')"
  [ "$actual" = "$EXPECTED_ACTIONLINT_VERSION" ]
}

host_golangci_lint_is_pinned() {
  local actual
  actual="$(golangci-lint version 2>/dev/null | awk '{ for (i = 1; i <= NF; i++) if ($i == "version") { print $(i + 1); exit } }')"
  [ "$actual" = "$EXPECTED_GOLANGCI_LINT_VERSION" ]
}

copy_repo_to_container() {
  local container_id="$1"
  tar \
    --exclude=.env \
    --exclude=.git \
    --exclude=.tools \
    --exclude='gha-creds-*.json' \
    --exclude=secrets \
    --exclude=terraform/.terraform \
    --exclude=site \
    --exclude='web/node_modules*' \
    --exclude=web/dist \
    --exclude='mirador-scribe/node_modules*' \
    --exclude=mirador-scribe/dist \
    -C "$ROOT_DIR" -cf - . | docker cp - "${container_id}:/repo"
}

shell_scripts=()
while IFS= read -r shell_script; do
  shell_scripts+=("${shell_script}")
done < <(rg --files -g '*.sh')
if ((${#shell_scripts[@]} > 0)); then
  echo "Running ShellCheck..."
  if command -v shellcheck >/dev/null 2>&1 && host_shellcheck_is_pinned; then
    shellcheck "${shell_scripts[@]}"
  elif command -v docker >/dev/null 2>&1; then
    if command -v shellcheck >/dev/null 2>&1; then
      echo "Installed ShellCheck is not ${EXPECTED_SHELLCHECK_VERSION}; using the pinned container." >&2
    fi
    shellcheck_image="koalaman/shellcheck:v0.11.0@sha256:61862eba1fcf09a484ebcc6feea46f1782532571a34ed51fedf90dd25f925a8d"
    shellcheck_id="$(docker create -w /repo "${shellcheck_image}" "${shell_scripts[@]}")"
    if ! copy_repo_to_container "${shellcheck_id}"; then
      docker rm -f "${shellcheck_id}" >/dev/null 2>&1 || true
      exit 1
    fi
    set +e
    docker start -a "${shellcheck_id}"
    shellcheck_status=$?
    set -e
    docker rm -f "${shellcheck_id}" >/dev/null
    if [ "${shellcheck_status}" -ne 0 ]; then exit "${shellcheck_status}"; fi
  else
    echo "Error: ShellCheck ${EXPECTED_SHELLCHECK_VERSION} or Docker is required." >&2
    exit 127
  fi
fi

echo "Running actionlint..."
workflow_files=()
while IFS= read -r workflow_file; do
  workflow_files+=("${workflow_file}")
done < <(rg --files .github/workflows -g '*.yaml' -g '*.yml')
if ((${#workflow_files[@]} == 0)); then
  echo "Error: no GitHub Actions workflow files were found." >&2
  exit 1
fi
if command -v actionlint >/dev/null 2>&1 && host_actionlint_is_pinned &&
  command -v shellcheck >/dev/null 2>&1 && host_shellcheck_is_pinned; then
  actionlint -config-file "${ACTIONLINT_CONFIG}" "${workflow_files[@]}"
elif command -v docker >/dev/null 2>&1; then
  if command -v actionlint >/dev/null 2>&1 || command -v shellcheck >/dev/null 2>&1; then
    echo "Installed actionlint/ShellCheck pair is not ${EXPECTED_ACTIONLINT_VERSION}/${EXPECTED_SHELLCHECK_VERSION}; using the pinned container." >&2
  fi
  actionlint_image="rhysd/actionlint:1.7.12@sha256:b1934ee5f1c509618f2508e6eb47ee0d3520686341fec936f3b79331f9315667"
  actionlint_id="$(
    docker create -w /repo "${actionlint_image}" \
      -config-file "${ACTIONLINT_CONFIG}" \
      "${workflow_files[@]}"
  )"
  if ! copy_repo_to_container "${actionlint_id}"; then
    docker rm -f "${actionlint_id}" >/dev/null 2>&1 || true
    exit 1
  fi
  set +e
  docker start -a "${actionlint_id}"
  actionlint_status=$?
  set -e
  docker rm -f "${actionlint_id}" >/dev/null
  if [ "${actionlint_status}" -ne 0 ]; then exit "${actionlint_status}"; fi
else
  echo "Error: actionlint ${EXPECTED_ACTIONLINT_VERSION} or Docker is required." >&2
  exit 127
fi

echo "Running golangci-lint..."
if command -v golangci-lint >/dev/null 2>&1 && host_golangci_lint_is_pinned; then
  golangci-lint run
  exit 0
fi

if command -v golangci-lint >/dev/null 2>&1; then
  echo "Installed golangci-lint is not ${EXPECTED_GOLANGCI_LINT_VERSION}; using the pinned container." >&2
fi
if ! command -v docker >/dev/null 2>&1; then
  echo "Error: golangci-lint ${EXPECTED_GOLANGCI_LINT_VERSION} or Docker is required." >&2
  exit 127
fi

container_id="$(
  docker create \
    --mount "type=volume,src=scribe-go-test-build-cache-v1,dst=/root/.cache/go-build" \
    --mount "type=volume,src=scribe-go-test-mod-cache-v1,dst=/go/pkg/mod" \
    --mount "type=volume,src=scribe-golangci-cache-v1,dst=/root/.cache/golangci-lint" \
    -w /app \
    "${GOLANGCI_IMAGE:?GOLANGCI_IMAGE is required}" \
    golangci-lint run
)"
cleanup() {
  docker rm -f "$container_id" >/dev/null 2>&1 || true
}
trap cleanup EXIT

tar \
  --exclude=.env \
  --exclude=.git \
  --exclude=.tools \
  --exclude='gha-creds-*.json' \
  --exclude=secrets \
  --exclude=terraform/.terraform \
  --exclude=site \
  --exclude='web/node_modules*' \
  --exclude=web/dist \
  --exclude='mirador-scribe/node_modules*' \
  --exclude=mirador-scribe/dist \
  -C "$ROOT_DIR" -cf - . | docker cp - "$container_id:/app"
docker start -a "$container_id"
