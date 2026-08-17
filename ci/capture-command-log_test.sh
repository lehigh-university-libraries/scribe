#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d)"
trap 'rm -rf "$TEST_DIR"' EXIT

fail() {
  echo "command-log capture contract failed: $*" >&2
  exit 1
}

helper="$ROOT_DIR/ci/capture-command-log.sh"
artifact="$TEST_DIR/terraform artifact.log"
runner_log="$TEST_DIR/runner.log"
first_mask='hvs.A+B[1]*?^$-test'
second_mask='root-token_test.two'

set +e
# shellcheck disable=SC2016 # The wrapped shell receives these values through its environment.
GITHUB_ACTIONS=true FIRST_MASK="$first_mask" SECOND_MASK="$second_mask" \
  "$helper" "$artifact" bash -c '
    printf "ordinary stdout before masking without a newline"
    printf "\n::add-mask::%s\n" "$FIRST_MASK" >&9
    printf "\n::add-mask::%s\n" "$FIRST_MASK"
    printf "stdout contains %s twice: %s\n" "$FIRST_MASK" "$FIRST_MASK"
    printf "\n::add-mask::%s\n" "$SECOND_MASK" >&9
    printf "\n::add-mask::%s\n" "$SECOND_MASK" >&2
    printf "stderr contains both %s and %s\n" "$FIRST_MASK" "$SECOND_MASK" >&2
    exit 23
  ' >"$runner_log"
status="$?"
set -e

[ "$status" -eq 23 ] || fail "the wrapped command exit status was not preserved"
grep -Fx "ordinary stdout before masking without a newline" "$artifact" >/dev/null ||
  fail "unterminated ordinary output was not safely separated from the mask"
grep -Fx "stdout contains *** twice: ***" "$artifact" >/dev/null ||
  fail "a repeated registered value was not redacted"
grep -Fx "stderr contains both *** and ***" "$artifact" >/dev/null ||
  fail "registered values from stdout and stderr were not redacted"
if grep -F '::add-mask::' "$artifact" >/dev/null; then
  fail "a GitHub masking workflow command was persisted in the artifact"
fi
for secret in "$first_mask" "$second_mask"; do
  if grep -F "$secret" "$artifact" >/dev/null; then
    fail "a registered value was persisted in the artifact"
  fi
  grep -Fx "::add-mask::${secret}" "$runner_log" >/dev/null ||
    fail "the GitHub runner did not receive a masking workflow command"
done

local_artifact="$TEST_DIR/local.log"
local_runner_log="$TEST_DIR/local-runner.log"
"$helper" "$local_artifact" bash -c '
  printf "local stdout\n"
  printf "local stderr\n" >&2
' >"$local_runner_log"
cmp -s "$local_artifact" "$local_runner_log" ||
  fail "ordinary local output differs between the terminal and captured log"

echo "Command-log masking behavior passed."
