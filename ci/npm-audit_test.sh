#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d)"
trap 'rm -rf "$TEST_DIR"' EXIT
mkdir -p "$TEST_DIR/bin" "$TEST_DIR/package"
: >"$TEST_DIR/package/package-lock.json"

cat >"$TEST_DIR/bin/npm" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
attempt=0
if [ -s "${FAKE_NPM_ATTEMPT_FILE}" ]; then
  read -r attempt <"${FAKE_NPM_ATTEMPT_FILE}"
fi
attempt=$((attempt + 1))
printf '%s\n' "$attempt" >"${FAKE_NPM_ATTEMPT_FILE}"
printf '%s\n' "$*" >>"${FAKE_NPM_ARGUMENT_LOG}"
if [ "$attempt" -lt "${FAKE_NPM_SUCCEED_AT}" ]; then
  exit "${FAKE_NPM_FAILURE_STATUS}"
fi
EOF

cat >"$TEST_DIR/bin/sleep" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${FAKE_SLEEP_LOG}"
EOF
chmod +x "$TEST_DIR/bin/npm" "$TEST_DIR/bin/sleep"

run_audit() {
  local succeed_at="$1" failure_status="$2" result=0
  : >"$TEST_DIR/attempts"
  : >"$TEST_DIR/arguments"
  : >"$TEST_DIR/sleeps"
  PATH="$TEST_DIR/bin:$PATH" \
    NPM_AUDIT_BIN="$TEST_DIR/bin/npm" \
    FAKE_NPM_ATTEMPT_FILE="$TEST_DIR/attempts" \
    FAKE_NPM_ARGUMENT_LOG="$TEST_DIR/arguments" \
    FAKE_NPM_SUCCEED_AT="$succeed_at" \
    FAKE_NPM_FAILURE_STATUS="$failure_status" \
    FAKE_SLEEP_LOG="$TEST_DIR/sleeps" \
    "$ROOT_DIR/ci/npm-audit.sh" "$TEST_DIR/package" \
    >"$TEST_DIR/stdout" 2>"$TEST_DIR/stderr" || result=$?
  return "$result"
}

run_audit 1 41
[ "$(cat "$TEST_DIR/attempts")" -eq 1 ]
[ ! -s "$TEST_DIR/sleeps" ]

run_audit 3 42
[ "$(cat "$TEST_DIR/attempts")" -eq 3 ]
[ "$(wc -l <"$TEST_DIR/sleeps")" -eq 2 ]
[ "$(grep -Fc -- '--audit-level=moderate --fetch-retries=0 --fetch-timeout=60000' "$TEST_DIR/arguments")" -eq 3 ]
[ "$(grep -Fc 'retrying.' "$TEST_DIR/stderr")" -eq 2 ]

if run_audit 4 43; then
  echo "npm audit retry hid a persistent failure" >&2
  exit 1
else
  status=$?
fi
[ "$status" -eq 43 ]
[ "$(cat "$TEST_DIR/attempts")" -eq 3 ]

if "$ROOT_DIR/ci/npm-audit.sh" "$TEST_DIR/missing" >/dev/null 2>&1; then
  echo "npm audit accepted a directory without a package lock" >&2
  exit 1
fi

echo "npm audit bounded retry behavior passed."
