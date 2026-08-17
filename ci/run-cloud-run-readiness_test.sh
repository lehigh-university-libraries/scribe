#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d "${TMPDIR:-/tmp}/scribe-cloud-run-readiness-launcher-test.XXXXXX")"
trap 'rm -rf -- "$TEST_DIR"' EXIT

fail() {
  echo "Cloud Run readiness launcher contract failed: $*" >&2
  exit 1
}

launcher="$ROOT_DIR/ci/run-cloud-run-readiness.sh"
fake_binary="$TEST_DIR/cloud-run-readiness"
argv_file="$TEST_DIR/argv"
marker_file="$TEST_DIR/invoked"

cat >"$fake_binary" <<'FAKE_READINESS'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\0' "$@" >"$SCRIBE_TEST_ARGV_FILE"
: >"$SCRIBE_TEST_MARKER_FILE"
printf 'typed readiness stdout\n'
printf 'typed readiness stderr\n' >&2
exit "${SCRIBE_TEST_EXIT_STATUS:?}"
FAKE_READINESS
chmod 0755 "$fake_binary"

set +e
SCRIBE_CLOUD_RUN_READINESS_BIN="$fake_binary" \
SCRIBE_TEST_ARGV_FILE="$argv_file" \
SCRIBE_TEST_MARKER_FILE="$marker_file" \
SCRIBE_TEST_EXIT_STATUS=37 \
  "$launcher" --preflight-only "job with spaces" browser "--literal=*" "" \
    >"$TEST_DIR/stdout" 2>"$TEST_DIR/stderr"
launcher_status=$?
set -e

[[ "$launcher_status" -eq 37 ]] || fail "launcher did not preserve the typed command exit status"
printf '%s\0' --preflight-only "job with spaces" browser "--literal=*" "" >"$TEST_DIR/expected-argv"
cmp -s "$TEST_DIR/expected-argv" "$argv_file" || fail "launcher changed command arguments"
grep -Fxq 'typed readiness stdout' "$TEST_DIR/stdout" || fail "launcher changed command stdout"
grep -Fxq 'typed readiness stderr' "$TEST_DIR/stderr" || fail "launcher changed command stderr"
[[ -e "$marker_file" ]] || fail "launcher did not execute the configured typed command"

assert_rejected() {
  local candidate="$1" label="$2" status

  rm -f -- "$marker_file"
  set +e
  SCRIBE_CLOUD_RUN_READINESS_BIN="$candidate" \
  SCRIBE_TEST_ARGV_FILE="$argv_file" \
  SCRIBE_TEST_MARKER_FILE="$marker_file" \
  SCRIBE_TEST_EXIT_STATUS=0 \
    "$launcher" backend >"$TEST_DIR/rejected.out" 2>"$TEST_DIR/rejected.err"
  status=$?
  set -e
  [[ "$status" -eq 2 ]] || fail "$label binary returned status $status instead of 2"
  [[ ! -e "$marker_file" ]] || fail "$label binary was executed"
  grep -Fq 'SCRIBE_CLOUD_RUN_READINESS_BIN must identify an absolute executable regular file' \
    "$TEST_DIR/rejected.err" || fail "$label binary rejection was not bounded"
}

set +e
env -u SCRIBE_CLOUD_RUN_READINESS_BIN "$launcher" \
  >"$TEST_DIR/missing-env.out" 2>"$TEST_DIR/missing-env.err"
missing_env_status=$?
set -e
[[ "$missing_env_status" -eq 2 ]] || fail "missing binary environment returned status $missing_env_status instead of 2"

assert_rejected "$TEST_DIR/missing" missing
ln -s "$fake_binary" "$TEST_DIR/cloud-run-readiness-link"
assert_rejected "$TEST_DIR/cloud-run-readiness-link" symlink
assert_rejected "relative/cloud-run-readiness" relative

non_executable="$TEST_DIR/cloud-run-readiness-not-executable"
install -m 0644 "$fake_binary" "$non_executable"
assert_rejected "$non_executable" non-executable

echo "Cloud Run readiness launcher validates one typed binary and preserves argv, streams, and exit status."
