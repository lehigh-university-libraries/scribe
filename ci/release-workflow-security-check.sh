#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKFLOW_DIR="${1:-${ROOT_DIR}/.github/workflows}"
RELEASE_WORKFLOW="${WORKFLOW_DIR}/github-release.yaml"
TERRAFORM_APPLY_WORKFLOW="${WORKFLOW_DIR}/terraform-apply.yaml"

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
[ -f "$TERRAFORM_APPLY_WORKFLOW" ] || fail "terraform-apply.yaml is required"
[ ! -e "${WORKFLOW_DIR}/goreleaser.yaml" ] || fail "legacy credentialed goreleaser.yaml must not exist"

while IFS= read -r workflow; do
  events="$(event_block "$workflow")"
  if rg -q 'workflow_dispatch:' <<<"$events" || rg -q '^[[:space:]]+tags:' <<<"$events"; then
    if rg -q 'contents:[[:space:]]*write|HOMEBREW_REPO_GHAT|goreleaser/goreleaser-action|autotag-dev/autotag' "$workflow"; then
      fail "$workflow exposes release authority from an arbitrary-ref workflow_dispatch or pushed-tag event"
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
trigger_names="$(
  sed -n 's/^  \([a-z_][a-z_]*\):.*/\1/p' <<<"$events" | LC_ALL=C sort
)"
[ "$trigger_names" = $'pull_request_target\nrepository_dispatch' ] ||
  fail "github-release.yaml may use only pull_request_target and repository_dispatch triggers"
require_pattern '^  pull_request_target:' "$RELEASE_WORKFLOW"
require_pattern '^    branches:' "$RELEASE_WORKFLOW"
require_pattern '^      - main$' "$RELEASE_WORKFLOW"
require_pattern '^    types:' "$RELEASE_WORKFLOW"
require_pattern '^      - closed$' "$RELEASE_WORKFLOW"
require_pattern '^  repository_dispatch:$' "$RELEASE_WORKFLOW"
repository_dispatch_block="$(
  awk '
    /^  repository_dispatch:[[:space:]]*$/ { in_dispatch = 1; next }
    in_dispatch && /^  [^[:space:]]/ { exit }
    in_dispatch { print }
  ' <<<"$events"
)"
release_dispatch_types="$(sed -n 's/^      - //p' <<<"$repository_dispatch_block")"
[ "$release_dispatch_types" = "release-main" ] ||
  fail "github-release.yaml repository_dispatch must accept only release-main"

require_pattern "github.event_name == 'repository_dispatch'" "$RELEASE_WORKFLOW"
require_pattern "github.event_name == 'pull_request_target'" "$RELEASE_WORKFLOW"
require_pattern 'github.event.pull_request.merged == true' "$RELEASE_WORKFLOW"
require_pattern 'github.event.client_payload.release_sha' "$RELEASE_WORKFLOW"
require_pattern 'github.event.client_payload.release_tag' "$RELEASE_WORKFLOW"
require_pattern '^    environment: release$' "$RELEASE_WORKFLOW"
require_pattern '^      actions: read$' "$RELEASE_WORKFLOW"
require_pattern '^      contents: write$' "$RELEASE_WORKFLOW"
require_pattern '^    timeout-minutes: 220$' "$RELEASE_WORKFLOW"
require_pattern '^  group: trusted-main-release$' "$RELEASE_WORKFLOW"
require_pattern '^  cancel-in-progress: false$' "$RELEASE_WORKFLOW"
forbid_pattern 'actions:[[:space:]]*write|gh workflow run|lehigh-university-libraries/gha/.github/workflows/bump-release|git push' "$RELEASE_WORKFLOW"

checkout_count="$(rg -c 'uses: actions/checkout@[0-9a-f]{40}' "$RELEASE_WORKFLOW" || true)"
[ "$checkout_count" = "1" ] || fail "release workflow must have one commit-pinned checkout"
require_pattern '^          ref: refs/heads/main$' "$RELEASE_WORKFLOW"
require_pattern '^          fetch-depth: 0$' "$RELEASE_WORKFLOW"
require_pattern '^          persist-credentials: false$' "$RELEASE_WORKFLOW"
forbid_pattern 'ref:.*\$\{\{|pull_request\.head|refs/pull/' "$RELEASE_WORKFLOW"

require_pattern 'GITHUB_REF.*refs/heads/main' "$RELEASE_WORKFLOW"
require_pattern 'main_sha=.*git rev-parse refs/remotes/origin/main' "$RELEASE_WORKFLOW"
require_pattern 'git merge-base --is-ancestor.*RELEASE_SHA.*main_sha' "$RELEASE_WORKFLOW"
require_pattern 'test.*RELEASE_SHA.*GITHUB_SHA' "$RELEASE_WORKFLOW"
require_pattern 'test.*RELEASE_SHA.*main_sha' "$RELEASE_WORKFLOW"
require_pattern 'EXPECTED_RELEASE_TAG.*\^\[0-9\].*\[0-9\].*\[0-9\].*\$' "$RELEASE_WORKFLOW"
require_pattern 'git checkout --detach.*RELEASE_SHA' "$RELEASE_WORKFLOW"
require_pattern 'git branch --force release-candidate.*RELEASE_SHA' "$RELEASE_WORKFLOW"
# shellcheck disable=SC2016 # This is an rg pattern for literal workflow syntax.
require_pattern 'expected_release_tag=\$EXPECTED_RELEASE_TAG' "$RELEASE_WORKFLOW"
# shellcheck disable=SC2016 # This is an rg pattern for literal workflow syntax.
require_pattern 'release_sha=\$RELEASE_SHA' "$RELEASE_WORKFLOW"
require_pattern 'run: \./ci/verify-release-source-run\.sh' "$RELEASE_WORKFLOW"
require_pattern 'state="\$\(\./ci/release-coverage\.sh\)"' "$RELEASE_WORKFLOW"

require_pattern '^  push:$' "$TERRAFORM_APPLY_WORKFLOW"
require_pattern '^      - main$' "$TERRAFORM_APPLY_WORKFLOW"
require_pattern '^  lint-test:$' "$TERRAFORM_APPLY_WORKFLOW"
require_pattern 'uses: \./\.github/workflows/lint-test\.yaml' "$TERRAFORM_APPLY_WORKFLOW"
terraform_apply_job="$(
  sed -n '/^  terraform-apply:/,/^  terraform-plan:/p' "$TERRAFORM_APPLY_WORKFLOW"
)"
for production_gate in \
  '      always() &&' \
  "      (github.event_name == 'push' || inputs.mode == 'apply') &&" \
  "      needs.prepare-backend-origin.result == 'success' &&" \
  "      needs.build-backend.result == 'success' &&" \
  "      needs.build-frontend.result == 'success' &&" \
  "      needs.foundation.result == 'success' &&" \
  "      needs.lint-test.result == 'success' &&" \
  "      needs.ocr-change-detection.result == 'success' &&" \
  "          needs.ocr-change-detection.outputs.ocr_changed == 'true' &&" \
  "          needs.build-ocr.result == 'success'" \
  "          needs.build-ocr.result == 'skipped' &&" \
  "          needs.ocr-change-detection.outputs.ocr_changed == 'false'"; do
  grep -Fqx "$production_gate" <<<"$terraform_apply_job" ||
    fail "terraform-apply.yaml production deployment is missing status-aware gate: ${production_gate}"
done
rg -q '^    needs: \[[^]]*lint-test[^]]*\]$' <<<"$terraform_apply_job" ||
  fail "terraform-apply.yaml production deployment must depend on repository CI"
# shellcheck disable=SC2016 # Match literal GitHub expression syntax.
rg -Fq "mode: \${{ github.event_name == 'push' && 'apply' || inputs.mode }}" <<<"$terraform_apply_job" ||
  fail "terraform-apply.yaml must map main pushes to protected apply mode"
# shellcheck disable=SC2016 # Match literal GitHub expressions in the workflow.
for production_binding in \
  'environment_name: production' \
  'tf_workspace: prod' \
  'site_name: scribe' \
  'docker_compose_branch: ${{ github.sha }}' \
  'checkout_ref: ${{ github.sha }}' \
  'image_tag: ${{ needs.build-backend.outputs.image }}' \
  'frontend_image_tag: ${{ needs.build-frontend.outputs.image }}' \
  'frontend_gar_image_tag: ${{ needs.prepare-backend-origin.outputs.frontend_gar_image_tag }}' \
  "ocr_images_json: \${{ needs.ocr-change-detection.outputs.ocr_changed == 'true' && needs.build-ocr.outputs.images_json || needs.ocr-change-detection.outputs.ocr_images_json }}" \
  'region: ${{ needs.prepare-backend-origin.outputs.region }}' \
  'zone: ${{ needs.prepare-backend-origin.outputs.zone }}'; do
  [ "$(grep -Fxc "      ${production_binding}" <<<"$terraform_apply_job")" -eq 1 ] ||
    fail "terraform-apply.yaml production deployment is missing exact binding: ${production_binding}"
done
require_pattern 'AUTOTAG_VERSION: v1\.4\.1' "$RELEASE_WORKFLOW"
require_pattern 'AUTOTAG_SHA256: [0-9a-f]{64}' "$RELEASE_WORKFLOW"
require_pattern 'sha256sum -c -' "$RELEASE_WORKFLOW"
require_pattern 'tag="\$\(\./ci/release-tag\.sh\)"' "$RELEASE_WORKFLOW"
require_pattern 'AUTOTAG_BRANCH: release-candidate' "$RELEASE_WORKFLOW"
expected_tag_count="$(rg -c 'EXPECTED_RELEASE_TAG: \$\{\{ steps\.source\.outputs\.expected_release_tag \}\}' "$RELEASE_WORKFLOW" || true)"
[ "$expected_tag_count" = "2" ] ||
  fail "coverage and tag creation must receive the validated expected release tag"
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
[ "$coverage_count" = "4" ] ||
  fail "source verification, AutoTag, tag creation, and draft preparation must skip an already-covered merge"
coverage_line="$(rg -n -m 1 'release-coverage\.sh' "$RELEASE_WORKFLOW" | cut -d: -f1)"
source_run_line="$(rg -n -m 1 'verify-release-source-run\.sh' "$RELEASE_WORKFLOW" | cut -d: -f1)"
prepare_line="$(rg -n -m 1 'release-draft\.sh prepare' "$RELEASE_WORKFLOW" | cut -d: -f1)"
goreleaser_line="$(rg -n -m 1 'uses: goreleaser/goreleaser-action@' "$RELEASE_WORKFLOW" | cut -d: -f1)"
publish_line="$(rg -n -m 1 'release-draft\.sh publish' "$RELEASE_WORKFLOW" | cut -d: -f1)"
[ "$coverage_line" -lt "$source_run_line" ] && [ "$source_run_line" -lt "$prepare_line" ] && [ "$prepare_line" -lt "$goreleaser_line" ] && [ "$goreleaser_line" -lt "$publish_line" ] ||
  fail "the exact draft must be prepared before GoReleaser and published only afterward"

echo "Release credentials are reachable only from reviewed main through merged PRs or exact typed default-branch dispatches."
