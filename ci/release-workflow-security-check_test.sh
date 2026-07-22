#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d)"
trap 'rm -rf "$TEST_DIR"' EXIT

checker="$ROOT_DIR/ci/release-workflow-security-check.sh"
source_workflows="$ROOT_DIR/.github/workflows"

new_fixture() {
  local name="$1" fixture
  fixture="$TEST_DIR/$name"
  mkdir -p "$fixture"
  cp -a "$source_workflows/." "$fixture/"
  printf '%s\n' "$fixture"
}

expect_failure() {
  local fixture="$1" expected="$2"
  if "$checker" "$fixture" >"$TEST_DIR/failure.out" 2>"$TEST_DIR/failure.err"; then
    echo "release workflow security check accepted an unsafe fixture" >&2
    exit 1
  fi
  grep -F "$expected" "$TEST_DIR/failure.err" >/dev/null
}

"$checker" "$source_workflows" >/dev/null

fixture="$(new_fixture dispatch)"
sed -i '/^on:$/a\  workflow_dispatch:' "$fixture/github-release.yaml"
expect_failure "$fixture" "dispatch or pushed-tag event"

fixture="$(new_fixture pushed_tag)"
cat >"$fixture/tag-release.yaml" <<'EOF'
name: Unsafe tag release
on:
  push:
    tags:
      - "[0-9]+.[0-9]+.[0-9]+"
permissions:
  contents: write
jobs:
  release:
    runs-on: ubuntu-24.04
    steps:
      - run: echo unsafe
        env:
          HOMEBREW_REPO_TOKEN: ${{ secrets.HOMEBREW_REPO_GHAT }}
EOF
expect_failure "$fixture" "dispatch or pushed-tag event"

fixture="$(new_fixture head_checkout)"
sed -i 's#ref: refs/heads/main#ref: ${{ github.event.pull_request.head.sha }}#' \
  "$fixture/github-release.yaml"
expect_failure "$fixture" "missing required pattern: ^          ref: refs/heads/main$"

fixture="$(new_fixture descendant_release)"
# shellcheck disable=SC2016 # This is a sed pattern for literal workflow syntax.
sed -i '/git checkout --detach "\$MERGED_SHA"/d' "$fixture/github-release.yaml"
expect_failure "$fixture" "missing required pattern: git checkout --detach.*MERGED_SHA"

fixture="$(new_fixture unconditional_publishers)"
sed -i "/if: steps.release.outputs.publish_required == 'true'/d" "$fixture/github-release.yaml"
expect_failure "$fixture" "setup, GoReleaser, and final publication must all skip an already-published release"

fixture="$(new_fixture unconditional_out_of_order)"
sed -i "/if: steps.coverage.outputs.release_required == 'true'/d" "$fixture/github-release.yaml"
expect_failure "$fixture" "AutoTag, tag creation, and draft preparation must skip an already-covered merge"

fixture="$(new_fixture legacy_workflow)"
printf '%s\n' 'name: legacy release' >"$fixture/goreleaser.yaml"
expect_failure "$fixture" "legacy credentialed goreleaser.yaml must not exist"

echo "Release workflow regression rejects dispatch, pushed-tag, PR-head checkout, and legacy credentialed release surfaces."
