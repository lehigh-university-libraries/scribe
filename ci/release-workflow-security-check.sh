#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKFLOW_DIR="${1:-${ROOT_DIR}/.github/workflows}"
RELEASE_WORKFLOW="${WORKFLOW_DIR}/github-release.yaml"

fail() {
  echo "release workflow security check failed: $*" >&2
  exit 1
}

require_pattern() {
  local pattern="$1" file="$2"
  rg -q -- "$pattern" "$file" || fail "$file is missing required pattern: $pattern"
}

forbid_pattern() {
  local pattern="$1" file="$2"
  if rg -q -- "$pattern" "$file"; then
    fail "$file contains forbidden pattern: $pattern"
  fi
}

event_block() {
  awk '
    /^on:[[:space:]]*$/ { in_events = 1; next }
    in_events && /^[^[:space:]#]/ { exit }
    in_events { print }
  ' "$1"
}

[ -d "$WORKFLOW_DIR" ] || fail "workflow directory does not exist: $WORKFLOW_DIR"
[ -f "$RELEASE_WORKFLOW" ] || fail "github-release.yaml is required"
[ ! -e "${WORKFLOW_DIR}/goreleaser.yaml" ] || fail "legacy credentialed goreleaser.yaml must not exist"

while IFS= read -r workflow; do
  events="$(event_block "$workflow")"
  if rg -q 'workflow_dispatch:' <<<"$events" || rg -q '^[[:space:]]+tags:' <<<"$events"; then
    if rg -q 'contents:[[:space:]]*write|HOMEBREW_REPO_GHAT|goreleaser/goreleaser-action|autotag-dev/autotag' "$workflow"; then
      fail "$workflow exposes release authority from a dispatch or pushed-tag event"
    fi
  fi
done < <(find "$WORKFLOW_DIR" -maxdepth 1 -type f \( -name '*.yaml' -o -name '*.yml' \) -print)

for sensitive_pattern in \
  'contents:[[:space:]]*write' \
  'HOMEBREW_REPO_GHAT' \
  'goreleaser/goreleaser-action' \
  'autotag-dev/autotag'; do
  while IFS= read -r workflow; do
    [ "$workflow" = "$RELEASE_WORKFLOW" ] ||
      fail "$workflow exposes release authority outside github-release.yaml"
  done < <(rg -l -g '*.yaml' -g '*.yml' -- "$sensitive_pattern" "$WORKFLOW_DIR" || true)
done

events="$(event_block "$RELEASE_WORKFLOW")"
require_pattern '^  pull_request_target:' "$RELEASE_WORKFLOW"
require_pattern '^    branches:' "$RELEASE_WORKFLOW"
require_pattern '^      - main$' "$RELEASE_WORKFLOW"
require_pattern '^    types:' "$RELEASE_WORKFLOW"
require_pattern '^      - closed$' "$RELEASE_WORKFLOW"
if rg -q 'workflow_dispatch:|^[[:space:]]+push:|^[[:space:]]+tags:' <<<"$events"; then
  fail "github-release.yaml must be triggered only by a merged main pull_request_target event"
fi

require_pattern "if: github.event.pull_request.merged == true" "$RELEASE_WORKFLOW"
require_pattern '^    environment: release$' "$RELEASE_WORKFLOW"
require_pattern '^      contents: write$' "$RELEASE_WORKFLOW"
require_pattern '^  group: trusted-main-release$' "$RELEASE_WORKFLOW"
require_pattern '^  cancel-in-progress: false$' "$RELEASE_WORKFLOW"
forbid_pattern 'actions:[[:space:]]*write|gh workflow run|lehigh-university-libraries/gha/.github/workflows/bump-release|git push' "$RELEASE_WORKFLOW"

checkout_count="$(rg -c 'uses: actions/checkout@[0-9a-f]{40}' "$RELEASE_WORKFLOW" || true)"
[ "$checkout_count" = "1" ] || fail "release workflow must have one commit-pinned checkout"
require_pattern '^          ref: refs/heads/main$' "$RELEASE_WORKFLOW"
require_pattern '^          fetch-depth: 0$' "$RELEASE_WORKFLOW"
require_pattern '^          persist-credentials: false$' "$RELEASE_WORKFLOW"
forbid_pattern 'ref:.*\$\{\{|pull_request\.head|refs/pull/' "$RELEASE_WORKFLOW"

require_pattern 'GITHUB_EVENT_NAME.*pull_request_target' "$RELEASE_WORKFLOW"
require_pattern 'GITHUB_REF.*refs/heads/main' "$RELEASE_WORKFLOW"
require_pattern 'git merge-base --is-ancestor.*MERGED_SHA.*refs/remotes/origin/main' "$RELEASE_WORKFLOW"
require_pattern 'git checkout --detach.*MERGED_SHA' "$RELEASE_WORKFLOW"
require_pattern 'git branch --force release-candidate.*MERGED_SHA' "$RELEASE_WORKFLOW"
# shellcheck disable=SC2016 # This is an rg pattern for literal workflow syntax.
require_pattern 'release_sha=\$MERGED_SHA' "$RELEASE_WORKFLOW"
require_pattern 'state="\$\(\./ci/release-coverage\.sh\)"' "$RELEASE_WORKFLOW"
require_pattern 'AUTOTAG_VERSION: v1\.4\.1' "$RELEASE_WORKFLOW"
require_pattern 'AUTOTAG_SHA256: [0-9a-f]{64}' "$RELEASE_WORKFLOW"
require_pattern 'sha256sum -c -' "$RELEASE_WORKFLOW"
require_pattern 'tag="\$\(\./ci/release-tag\.sh\)"' "$RELEASE_WORKFLOW"
require_pattern 'AUTOTAG_BRANCH: release-candidate' "$RELEASE_WORKFLOW"
require_pattern 'state="\$\(\./ci/release-draft\.sh prepare\)"' "$RELEASE_WORKFLOW"
require_pattern 'uses: goreleaser/goreleaser-action@[0-9a-f]{40}' "$RELEASE_WORKFLOW"
require_pattern 'version: v2\.17\.0' "$RELEASE_WORKFLOW"
require_pattern 'GORELEASER_CURRENT_TAG: \$\{\{ steps\.tag\.outputs\.tag \}\}' "$RELEASE_WORKFLOW"
require_pattern 'GITHUB_TOKEN: \$\{\{ github\.token \}\}' "$RELEASE_WORKFLOW"
require_pattern 'HOMEBREW_REPO_TOKEN: \$\{\{ secrets\.HOMEBREW_REPO_GHAT \}\}' "$RELEASE_WORKFLOW"
require_pattern 'run: ./ci/release-draft\.sh publish' "$RELEASE_WORKFLOW"

conditional_count="$(rg -c "if: steps.release.outputs.publish_required == 'true'" "$RELEASE_WORKFLOW" || true)"
[ "$conditional_count" = "3" ] || fail "setup, GoReleaser, and final publication must all skip an already-published release"
coverage_count="$(rg -c "if: steps.coverage.outputs.release_required == 'true'" "$RELEASE_WORKFLOW" || true)"
[ "$coverage_count" = "3" ] || fail "AutoTag, tag creation, and draft preparation must skip an already-covered merge"
coverage_line="$(rg -n -m 1 'release-coverage\.sh' "$RELEASE_WORKFLOW" | cut -d: -f1)"
prepare_line="$(rg -n -m 1 'release-draft\.sh prepare' "$RELEASE_WORKFLOW" | cut -d: -f1)"
goreleaser_line="$(rg -n -m 1 'uses: goreleaser/goreleaser-action@' "$RELEASE_WORKFLOW" | cut -d: -f1)"
publish_line="$(rg -n -m 1 'release-draft\.sh publish' "$RELEASE_WORKFLOW" | cut -d: -f1)"
[ "$coverage_line" -lt "$prepare_line" ] && [ "$prepare_line" -lt "$goreleaser_line" ] && [ "$goreleaser_line" -lt "$publish_line" ] ||
  fail "the exact draft must be prepared before GoReleaser and published only afterward"

echo "Release credentials are reachable only from reviewed main after a merged PR; tag and dispatch events are inert."
