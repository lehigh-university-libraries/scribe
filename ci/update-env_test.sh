#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d)"
trap 'rm -rf "$TEST_DIR"' EXIT

env_file="$TEST_DIR/runtime.env"
expected_file="$TEST_DIR/expected.env"
original_file="$TEST_DIR/original.env"
printf '%s\n' 'KEEP=one' 'TARGET=old' 'TARGET=duplicate' 'TAIL=last' >"$env_file"
value=' spaced=value {"unicode":"✓"} '
encoded_value="$(printf '%s' "$value" | base64 | tr -d '\n')"
"$ROOT_DIR/scripts/update-env.sh" "$env_file" TARGET --base64 "$encoded_value"
printf '%s\n' 'KEEP=one' "TARGET=$value" 'TAIL=last' >"$expected_file"
cmp "$expected_file" "$env_file"

cp "$env_file" "$original_file"
assert_rejected_without_mutation() {
  if "$ROOT_DIR/scripts/update-env.sh" "$env_file" "$1" --base64 "$2" >/dev/null 2>&1; then
    echo "update-env accepted invalid input" >&2
    exit 1
  fi
  cmp "$original_file" "$env_file"
}

assert_rejected_without_mutation 'invalid-name' "$encoded_value"
assert_rejected_without_mutation 'TARGET' 'not-base64!'
assert_rejected_without_mutation 'TARGET' "$(printf 'line\nbreak' | base64 | tr -d '\n')"
assert_rejected_without_mutation 'TARGET' "$(printf 'nul\0byte' | base64 | tr -d '\n')"

new_file="$TEST_DIR/new.env"
"$ROOT_DIR/scripts/update-env.sh" "$new_file" NEW_VALUE --base64 "$(printf 'created' | base64 | tr -d '\n')"
printf '%s\n' 'NEW_VALUE=created' >"$expected_file"
cmp "$expected_file" "$new_file"

echo "Atomic Bash environment updates passed."
