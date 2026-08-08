#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly ROOT_DIR
readonly ENTRYPOINT="$ROOT_DIR/web/e2e/deployed-readiness-entrypoint.sh"
readonly STATE_PATH="/tmp/scribe-browser-session-state.json"

[ ! -e "$STATE_PATH" ] || {
  echo "entrypoint test refuses to replace an existing state path" >&2
  exit 1
}

test_dir="$(mktemp -d)"
trap 'rm -f -- "$STATE_PATH"; rm -rf -- "$test_dir"' EXIT
install -m 0755 "$ROOT_DIR/ci/testdata/fake-browser-readiness-node.sh" "$test_dir/node"

synthetic_state=$'{"cookies":[{"name":"synthetic"}],"origins":[]}\n'
read -r synthetic_state_sha _ < <(printf '%s' "$synthetic_state" | sha256sum)
PATH="$test_dir:$PATH" \
  SCRIBE_BROWSER_MODE=production \
  SCRIBE_BROWSER_STORAGE_STATE_JSON="$synthetic_state" \
  TEST_EXPECT_STATE=production \
  TEST_EXPECTED_STATE_SHA256="$synthetic_state_sha" \
  "$ENTRYPOINT"
[ ! -e "$STATE_PATH" ]

PATH="$test_dir:$PATH" \
  SCRIBE_BROWSER_MODE=preview \
  TEST_EXPECT_STATE=preview \
  "$ENTRYPOINT"

if PATH="$test_dir:$PATH" \
  SCRIBE_BROWSER_MODE=preview \
  SCRIBE_BROWSER_STORAGE_STATE_JSON="$synthetic_state" \
  TEST_EXPECT_STATE=preview \
  "$ENTRYPOINT" >/dev/null 2>&1; then
  echo "preview entrypoint accepted production state" >&2
  exit 1
fi

if PATH="$test_dir:$PATH" \
  SCRIBE_BROWSER_MODE=invalid \
  TEST_EXPECT_STATE=preview \
  "$ENTRYPOINT" >/dev/null 2>&1; then
  echo "entrypoint accepted an invalid mode" >&2
  exit 1
fi

install -m 0600 /dev/null "$STATE_PATH"
if PATH="$test_dir:$PATH" \
  SCRIBE_BROWSER_MODE=production \
  SCRIBE_BROWSER_STORAGE_STATE_JSON="$synthetic_state" \
  TEST_EXPECT_STATE=production \
  TEST_EXPECTED_STATE_SHA256="$synthetic_state_sha" \
  "$ENTRYPOINT" >/dev/null 2>&1; then
  echo "entrypoint replaced an existing state file" >&2
  exit 1
fi
[ ! -s "$STATE_PATH" ]

echo "Browser readiness entrypoint state handling passed."
