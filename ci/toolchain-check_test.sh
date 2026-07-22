#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d)"
trap 'rm -rf "${TEST_DIR}"' EXIT

mkdir -p "${TEST_DIR}/fixture/ci/nested" "${TEST_DIR}/fixture/.github/workflows" "${TEST_DIR}/bin"
cp "${ROOT_DIR}/ci/toolchain-check.sh" "${TEST_DIR}/fixture/ci/toolchain-check.sh"
chmod +x "${TEST_DIR}/fixture/ci/toolchain-check.sh"

cat >"${TEST_DIR}/fixture/.go-version" <<'EOF'
1.26.5
EOF
cat >"${TEST_DIR}/fixture/.nvmrc" <<'EOF'
24.18.0
EOF
cat >"${TEST_DIR}/fixture/.tool-versions" <<'EOF'
golang 1.26.5
nodejs 24.18.0
python 3.14.2
terraform 1.15.8
EOF
cat >"${TEST_DIR}/fixture/Dockerfile" <<'EOF'
FROM golang:1.26.5-alpine
EOF
cat >"${TEST_DIR}/fixture/Dockerfile.segmentor" <<'EOF'
FROM golang:1.26.5-alpine AS helper
FROM python:3.14.2-slim
EOF
cat >"${TEST_DIR}/fixture/Dockerfile.frontend" <<'EOF'
FROM node:24.18.0-alpine
EOF
cat >"${TEST_DIR}/fixture/Dockerfile.docs" <<'EOF'
FROM python:3.14.2-slim
EOF
cat >"${TEST_DIR}/fixture/ci/segmentor-lock.sh" <<'EOF'
python_image="python:3.14.2-slim"
EOF
cat >"${TEST_DIR}/fixture/ci/test-frontend.sh" <<'EOF'
FRONTEND_TEST_IMAGE="node:24.18.0-alpine"
EOF
cat >"${TEST_DIR}/fixture/ci/nested/version with spaces.sh" <<'EOF'
GO_TEST_IMAGE="golang:1.26.5-alpine"
EOF
cat >"${TEST_DIR}/fixture/.github/workflows/toolchain.yml" <<'EOF'
jobs:
  test:
    with:
      terraform_version: 1.15.8
EOF

for command_name in bash dirname tr awk grep find sed; do
  ln -s "$(command -v "${command_name}")" "${TEST_DIR}/bin/${command_name}"
done

if PATH="${TEST_DIR}/bin" command -v rg >/dev/null 2>&1; then
  echo "isolated bootstrap PATH unexpectedly contains rg" >&2
  exit 1
fi

PATH="${TEST_DIR}/bin" "${TEST_DIR}/fixture/ci/toolchain-check.sh" >/dev/null

stale_go_version="1.26.4"
awk -v version="${stale_go_version}" \
  'BEGIN { for (i = 0; i < 100000; i++) print "GO_TEST_IMAGE=\"golang:" version "-alpine\"" }' \
  >"${TEST_DIR}/fixture/ci/nested/many-stale-images.sh"
if PATH="${TEST_DIR}/bin" "${TEST_DIR}/fixture/ci/toolchain-check.sh" \
  >"${TEST_DIR}/go-mismatch.out" 2>"${TEST_DIR}/go-mismatch.err"; then
  echo "toolchain check accepted stale Go versions after a pipe buffer filled" >&2
  exit 1
fi
grep -Fq 'has a Go image that differs from .go-version' "${TEST_DIR}/go-mismatch.err"
rm "${TEST_DIR}/fixture/ci/nested/many-stale-images.sh"

sed -i.bak 's/terraform_version: 1\.15\.8/terraform_version: 1.15.7/' \
  "${TEST_DIR}/fixture/.github/workflows/toolchain.yml"
if PATH="${TEST_DIR}/bin" "${TEST_DIR}/fixture/ci/toolchain-check.sh" \
  >"${TEST_DIR}/mismatch.out" 2>"${TEST_DIR}/mismatch.err"; then
  echo "toolchain check accepted a stale Terraform version in a .yml workflow" >&2
  exit 1
fi
grep -Fq 'does not use Terraform 1.15.8' "${TEST_DIR}/mismatch.err"

echo "Toolchain contracts bootstrap without ripgrep and reject stale versions."
