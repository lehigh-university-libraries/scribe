#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d "${TMPDIR:-/tmp}/scribe-dependency-scan-test.XXXXXX")"
trap 'rm -rf -- "$TEST_DIR"' EXIT

fail() {
  echo "Dependency-scan path parity contract failed: $*" >&2
  exit 1
}

fixture="$TEST_DIR/repository"
mkdir -p "$fixture/ci" "$TEST_DIR/bin"
cp "$ROOT_DIR/ci/dependency-scan.sh" "$fixture/ci/dependency-scan.sh"
printf '%s\n' 'module example.test/dependency-scan-fixture' >"$fixture/go.mod"
git -C "$fixture" init -q
git -C "$fixture" config user.name "Dependency Scan Contract"
git -C "$fixture" config user.email "dependency-scan@example.invalid"
git -C "$fixture" add ci/dependency-scan.sh go.mod
git -C "$fixture" -c commit.gpgSign=false commit -qm "fixture"

cat >"$TEST_DIR/bin/trivy" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [[ "${1:-}" == "--version" ]]; then
  printf 'Version: %s\n' "${TEST_TRIVY_VERSION:?}"
  exit 0
fi
scan_root="${!#}"
(
  cd "$scan_root"
  find . -type f -print | sed 's#^\./##' | sort
) >"${TEST_TRIVY_FILES:?}"
EOF
chmod 0755 "$TEST_DIR/bin/trivy"

mkdir -p \
  "$fixture/secrets" \
  "$fixture/nested/.env" \
  "$fixture/nested/gha-creds-directory.json"
printf '%s\n' 'LOCAL_ONLY=value' >"$fixture/.env"
printf '%s\n' 'local-token' >"$fixture/secrets/token"
printf '%s\n' '{}' >"$fixture/nested/gha-creds-local.json"
printf '%s\n' 'nested-local-token' >"$fixture/nested/.env/credential"
printf '%s\n' '{}' >"$fixture/nested/gha-creds-directory.json/key"

scan_files="$TEST_DIR/scanned-files"
trivy_version="$(sed -n 's/^readonly EXPECTED_TRIVY_VERSION="\([^"]*\)"$/\1/p' "$fixture/ci/dependency-scan.sh")"
[ -n "$trivy_version" ] || fail "the scanner does not declare its expected Trivy version"
PATH="$TEST_DIR/bin:/usr/bin:/bin" TEST_TRIVY_FILES="$scan_files" TEST_TRIVY_VERSION="$trivy_version" \
  bash "$fixture/ci/dependency-scan.sh"
grep -Fxq 'go.mod' "$scan_files" ||
  fail "the shared scan snapshot omitted a tracked source file"
for excluded in \
  .env \
  secrets/token \
  nested/.env/credential \
  nested/gha-creds-local.json \
  nested/gha-creds-directory.json/key; do
  if grep -Fxq "$excluded" "$scan_files"; then
    fail "the shared scan snapshot included untracked runtime secret $excluded"
  fi
done

for rejected in \
  .env \
  secrets/token \
  nested/.env/credential \
  nested/gha-creds-local.json \
  nested/gha-creds-directory.json/key; do
  git -C "$fixture" add -f "$rejected"
  if PATH="$TEST_DIR/bin:/usr/bin:/bin" TEST_TRIVY_FILES="$scan_files" TEST_TRIVY_VERSION="$trivy_version" \
    bash "$fixture/ci/dependency-scan.sh" >"$TEST_DIR/rejected.out" 2>&1; then
    fail "the scan accepted tracked runtime secret $rejected"
  fi
  grep -Fq 'Git tracks runtime-secret paths' "$TEST_DIR/rejected.out" ||
    fail "tracked runtime secret rejection omitted its bounded diagnostic"
  grep -Fq "  $rejected" "$TEST_DIR/rejected.out" ||
    fail "tracked runtime secret rejection omitted the affected path"
  git -C "$fixture" reset -q HEAD -- "$rejected"
done

echo "Host and container dependency scans share one runtime-secret-safe snapshot."
