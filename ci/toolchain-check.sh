#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

mode="${1:-files}"
case "$mode" in
  files|--host|--go) ;;
  *) echo "usage: $0 [files|--host|--go]" >&2; exit 2 ;;
esac

fail() {
  echo "toolchain contract failed: $*" >&2
  exit 1
}

go_version="$(tr -d '[:space:]' < .go-version)"
node_version="$(tr -d '[:space:]' < .nvmrc)"
tool_go="$(awk '$1 == "golang" { print $2 }' .tool-versions)"
tool_node="$(awk '$1 == "nodejs" { print $2 }' .tool-versions)"
tool_python="$(awk '$1 == "python" { print $2 }' .tool-versions)"
tool_terraform="$(awk '$1 == "terraform" { print $2 }' .tool-versions)"
go_version_re="${go_version//./\\.}"
node_version_re="${node_version//./\\.}"
python_version_re="${tool_python//./\\.}"
terraform_version_re="${tool_terraform//./\\.}"

[ "$go_version" = "$tool_go" ] || fail ".go-version and .tool-versions disagree"
[ "$node_version" = "$tool_node" ] || fail ".nvmrc and .tool-versions disagree"

for file in Dockerfile Dockerfile.segmentor; do
  grep -Eq "^FROM golang:${go_version_re}[-@]" "$file" || fail "$file does not use Go ${go_version}"
done
grep -Eq "^FROM node:${node_version_re}-" Dockerfile.frontend || fail "Dockerfile.frontend does not use Node ${node_version}"
grep -Eq "^FROM python:${python_version_re}-" Dockerfile.segmentor || fail "Dockerfile.segmentor does not use Python ${tool_python}"
grep -Eq "^FROM python:${python_version_re}-" Dockerfile.docs || fail "Dockerfile.docs does not use Python ${tool_python}"
grep -Eq "python_image=\"python:${python_version_re}-" ci/segmentor-lock.sh || fail "ci/segmentor-lock.sh does not use Python ${tool_python}"
grep -Eq "FRONTEND_TEST_IMAGE=.*node:${node_version_re}-" ci/test-frontend.sh || fail "ci/test-frontend.sh does not use Node ${node_version}"

while IFS= read -r -d '' file; do
  if grep -Eo 'golang:[0-9]+\.[0-9]+\.[0-9]+' "$file" |
    grep -Fvx "golang:${go_version}" >/dev/null; then
    fail "$file has a Go image that differs from .go-version"
  fi
done < <(find ci -type f -name '*.sh' -print0)

while IFS= read -r -d '' file; do
  if grep -Eq '^[[:space:]]*terraform_version:' "$file"; then
    if grep -E '^[[:space:]]*terraform_version:' "$file" |
      grep -Ev "^[[:space:]]*terraform_version:[[:space:]]*${terraform_version_re}[[:space:]]*$" >/dev/null; then
      fail "$file does not use Terraform ${tool_terraform}"
    fi
  fi
done < <(find .github/workflows -type f \( -name '*.yaml' -o -name '*.yml' \) -print0)

if [ "$mode" = "--go" ]; then
	go_bin="$(command -v go || true)"
	[ -n "$go_bin" ] || fail "Go ${go_version} is required; install it with mise/asdf using .tool-versions"
	installed_go="$("$go_bin" env GOVERSION | sed 's/^go//')"
	[ "$installed_go" = "$go_version" ] || fail "installed Go ${installed_go} does not match ${go_version}"
fi

if [ "$mode" = "--host" ]; then
	if command -v go >/dev/null 2>&1; then
		installed_go="$(go env GOVERSION | sed 's/^go//')"
		[ "$installed_go" = "$go_version" ] || fail "installed Go ${installed_go} does not match ${go_version}"
	fi
  if command -v node >/dev/null 2>&1; then
    installed_node="$(node --version | sed 's/^v//')"
    [ "$installed_node" = "$node_version" ] || fail "installed Node ${installed_node} does not match ${node_version}"
  fi
  if command -v terraform >/dev/null 2>&1; then
    installed_terraform="$(terraform version -json | jq -r '.terraform_version')"
    [ "$installed_terraform" = "$tool_terraform" ] || fail "installed Terraform ${installed_terraform} does not match ${tool_terraform}"
  fi
fi

echo "Toolchain version contracts passed."
