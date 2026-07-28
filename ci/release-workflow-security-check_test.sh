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
expect_failure "$fixture" "arbitrary-ref workflow_dispatch or pushed-tag event"

fixture="$(new_fixture broad_repository_dispatch)"
sed -i '/^      - release-main$/a\      - release-anything' "$fixture/github-release.yaml"
expect_failure "$fixture" "repository_dispatch must accept only release-main"

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
expect_failure "$fixture" "arbitrary-ref workflow_dispatch or pushed-tag event"

fixture="$(new_fixture head_checkout)"
sed -i 's#ref: refs/heads/main#ref: ${{ github.event.pull_request.head.sha }}#' \
  "$fixture/github-release.yaml"
expect_failure "$fixture" "missing required pattern: ^          ref: refs/heads/main$"

fixture="$(new_fixture dispatch_event_head)"
# shellcheck disable=SC2016 # Match literal shell variables in the workflow fixture.
sed -i '/test "\$RELEASE_SHA" = "\$GITHUB_SHA"/d' "$fixture/github-release.yaml"
expect_failure "$fixture" "missing required pattern: test.*RELEASE_SHA.*GITHUB_SHA"

fixture="$(new_fixture dispatch_current_main)"
# shellcheck disable=SC2016 # Match literal shell variables in the workflow fixture.
sed -i '/test "\$RELEASE_SHA" = "\$main_sha"/d' "$fixture/github-release.yaml"
expect_failure "$fixture" "missing required pattern: test.*RELEASE_SHA.*main_sha"

fixture="$(new_fixture no_actions_read)"
sed -i '/^      actions: read$/d' "$fixture/github-release.yaml"
expect_failure "$fixture" "missing required pattern: ^      actions: read$"

fixture="$(new_fixture no_source_run_gate)"
sed -i '\#run: ./ci/verify-release-source-run.sh#d' "$fixture/github-release.yaml"
expect_failure "$fixture" 'missing required pattern: run: \./ci/verify-release-source-run'

fixture="$(new_fixture release_without_ci_dependency)"
sed -i '/^    needs: .*lint-test/s/, lint-test//' "$fixture/terraform-apply.yaml"
expect_failure "$fixture" "production deployment must depend on repository CI"

fixture="$(new_fixture release_without_status_aware_gate)"
sed -i '/^      always() &&$/d' "$fixture/terraform-apply.yaml"
expect_failure "$fixture" "production deployment is missing status-aware gate:       always() &&"

fixture="$(new_fixture release_accepts_failed_ocr_detection)"
sed -i "s/needs\.ocr-change-detection\.result == 'success'/needs.ocr-change-detection.result != 'cancelled'/" \
  "$fixture/terraform-apply.yaml"
expect_failure "$fixture" "production deployment is missing status-aware gate:       needs.ocr-change-detection.result == 'success' &&"

fixture="$(new_fixture release_rebuild_skip_without_reuse_decision)"
sed -i "/needs\.ocr-change-detection\.outputs\.ocr_changed == 'false'/d" \
  "$fixture/terraform-apply.yaml"
expect_failure "$fixture" "production deployment is missing status-aware gate:           needs.ocr-change-detection.outputs.ocr_changed == 'false'"

fixture="$(new_fixture release_build_without_change_decision)"
sed -i "/needs\.ocr-change-detection\.outputs\.ocr_changed == 'true' &&/d" \
  "$fixture/terraform-apply.yaml"
expect_failure "$fixture" "production deployment is missing status-aware gate:           needs.ocr-change-detection.outputs.ocr_changed == 'true' &&"

fixture="$(new_fixture release_preview_environment)"
sed -i '/^      environment_name: production$/s/production/preview/' "$fixture/terraform-apply.yaml"
expect_failure "$fixture" "missing exact binding: environment_name: production"

fixture="$(new_fixture release_dev_workspace)"
sed -i '/^      tf_workspace: prod$/s/prod/dev/' "$fixture/terraform-apply.yaml"
expect_failure "$fixture" "missing exact binding: tf_workspace: prod"

fixture="$(new_fixture release_wrong_compose_source)"
# shellcheck disable=SC2016 # Match a literal GitHub expression in the workflow fixture.
sed -i 's#docker_compose_branch: \${{ github.sha }}#docker_compose_branch: 0000000000000000000000000000000000000000#' \
  "$fixture/terraform-apply.yaml"
# shellcheck disable=SC2016 # Match a literal GitHub expression in the expected diagnostic.
expect_failure "$fixture" 'missing exact binding: docker_compose_branch: ${{ github.sha }}'

fixture="$(new_fixture release_wrong_checkout_source)"
# shellcheck disable=SC2016 # Match a literal GitHub expression in the workflow fixture.
sed -i 's#checkout_ref: \${{ github.sha }}#checkout_ref: 0000000000000000000000000000000000000000#' \
  "$fixture/terraform-apply.yaml"
# shellcheck disable=SC2016 # Match a literal GitHub expression in the expected diagnostic.
expect_failure "$fixture" 'missing exact binding: checkout_ref: ${{ github.sha }}'

fixture="$(new_fixture release_commented_checkout_source)"
# shellcheck disable=SC2016 # Match a literal GitHub expression in the workflow fixture.
sed -i 's|      checkout_ref: \${{ github.sha }}|      # checkout_ref: ${{ github.sha }}\n      checkout_ref: 0000000000000000000000000000000000000000|' \
  "$fixture/terraform-apply.yaml"
# shellcheck disable=SC2016 # Match a literal GitHub expression in the expected diagnostic.
expect_failure "$fixture" 'missing exact binding: checkout_ref: ${{ github.sha }}'

fixture="$(new_fixture release_wrong_backend_artifact)"
# shellcheck disable=SC2016 # Match a literal GitHub expression in the workflow fixture.
sed -i 's#image_tag: \${{ needs.build-backend.outputs.image }}#image_tag: ghcr.io/example/stale@sha256:0000#' \
  "$fixture/terraform-apply.yaml"
# shellcheck disable=SC2016 # Match a literal GitHub expression in the expected diagnostic.
expect_failure "$fixture" 'missing exact binding: image_tag: ${{ needs.build-backend.outputs.image }}'

fixture="$(new_fixture release_wrong_ocr_artifacts)"
# shellcheck disable=SC2016 # Match a literal GitHub expression in the workflow fixture.
sed -i "s#ocr_images_json: \${{ needs.ocr-change-detection.outputs.ocr_changed == 'true' && needs.build-ocr.outputs.images_json || needs.ocr-change-detection.outputs.ocr_images_json }}#ocr_images_json: {}#" \
  "$fixture/terraform-apply.yaml"
# shellcheck disable=SC2016 # Match a literal GitHub expression in the expected diagnostic.
expect_failure "$fixture" "missing exact binding: ocr_images_json: \${{ needs.ocr-change-detection.outputs.ocr_changed == 'true' && needs.build-ocr.outputs.images_json || needs.ocr-change-detection.outputs.ocr_images_json }}"

fixture="$(new_fixture descendant_release)"
# shellcheck disable=SC2016 # Match a literal shell variable in the workflow fixture.
sed -i '/git checkout --detach "\$RELEASE_SHA"/d' "$fixture/github-release.yaml"
expect_failure "$fixture" "missing required pattern: git checkout --detach.*RELEASE_SHA"

fixture="$(new_fixture unpinned_expected_tag)"
sed -i '/EXPECTED_RELEASE_TAG: \${{ steps.source.outputs.expected_release_tag }}/d' \
  "$fixture/github-release.yaml"
expect_failure "$fixture" "coverage and tag creation must receive the validated expected release tag"

fixture="$(new_fixture unconditional_publishers)"
sed -i "/if: steps.release.outputs.publish_required == 'true'/d" "$fixture/github-release.yaml"
expect_failure "$fixture" "setup, GoReleaser, and final publication must all skip an already-published release"

fixture="$(new_fixture unconditional_out_of_order)"
sed -i "/if: steps.coverage.outputs.release_required == 'true'/d" "$fixture/github-release.yaml"
expect_failure "$fixture" "AutoTag, tag creation, and draft preparation must skip an already-covered merge"

fixture="$(new_fixture legacy_workflow)"
printf '%s\n' 'name: legacy release' >"$fixture/goreleaser.yaml"
expect_failure "$fixture" "legacy credentialed goreleaser.yaml must not exist"

echo "Release workflow regression permits only exact default-main dispatches and rejects broader credential surfaces."
