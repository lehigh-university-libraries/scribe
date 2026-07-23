#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fixture="$(mktemp -d)"
remote_fixture=""
checkout_fixture=""
cleanup() {
  rm -rf "$fixture"
  [ -z "$remote_fixture" ] || rm -rf "$remote_fixture"
  [ -z "$checkout_fixture" ] || rm -rf "$checkout_fixture"
}
trap cleanup EXIT

git -C "$fixture" init -q
git -C "$fixture" config user.name "Scribe Contract"
git -C "$fixture" config user.email "scribe-contract@example.invalid"

commit_file() {
  local contents="$1"
  local subject="$2"
  printf '%s\n' "$contents" >"$fixture/value"
  git -C "$fixture" add value
  git -C "$fixture" commit -q -m "$subject"
  git -C "$fixture" rev-parse HEAD
}

base_sha="$(commit_file base base)"
previous_sha="$(commit_file previous '[minor] Harden canonical IIIF processing and delivery')"
descendant_sha="$(commit_file descendant follow-up)"

"$ROOT_DIR/ci/verify-production-source-lineage.sh" "$fixture" "$previous_sha" "$descendant_sha"

git -C "$fixture" reset -q --hard "$base_sha"
before_sha="$(commit_file before '[minor] Harden canonical IIIF processing and delivery')"
git -C "$fixture" reset -q --hard "$base_sha"
current_sha="$(commit_file current '[minor] Harden canonical IIIF processing and delivery')"

verify_retry() {
  DEPLOY_EVENT_NAME=push \
    DEPLOY_EVENT_FORCED=true \
    DEPLOY_EVENT_BEFORE="$before_sha" \
    DEPLOY_EVENT_AFTER="$current_sha" \
    "$ROOT_DIR/ci/verify-production-source-lineage.sh" "$fixture" "$previous_sha" "$current_sha"
}

verify_retry

remote_fixture="$(mktemp -d)"
checkout_fixture="$(mktemp -d)"
git -C "$remote_fixture" init -q --bare
git -C "$remote_fixture" config uploadpack.allowReachableSHA1InWant true
git -C "$fixture" push -q "$remote_fixture" "$current_sha:refs/heads/main"
git -C "$fixture" push -q "$remote_fixture" "$previous_sha:refs/heads/previous-production"
git -C "$fixture" push -q "$remote_fixture" "$before_sha:refs/archive/force-push-before"
git clone -q --branch main "file://$remote_fixture" "$checkout_fixture"
git -C "$checkout_fixture" fetch -q origin previous-production
git -C "$checkout_fixture" branch previous-production FETCH_HEAD
if git -C "$checkout_fixture" cat-file -e "${before_sha}^{commit}" 2>/dev/null; then
  echo "lineage fetch fixture unexpectedly contains the orphan push-before commit" >&2
  exit 1
fi
DEPLOY_EVENT_NAME=push \
  DEPLOY_EVENT_FORCED=true \
  DEPLOY_EVENT_BEFORE="$before_sha" \
  DEPLOY_EVENT_AFTER="$current_sha" \
  "$ROOT_DIR/ci/verify-production-source-lineage.sh" "$checkout_fixture" "$previous_sha" "$current_sha"

if DEPLOY_EVENT_NAME=push DEPLOY_EVENT_FORCED=false DEPLOY_EVENT_BEFORE="$before_sha" DEPLOY_EVENT_AFTER="$current_sha" \
  "$ROOT_DIR/ci/verify-production-source-lineage.sh" "$fixture" "$previous_sha" "$current_sha" >/dev/null 2>&1; then
  echo "lineage verifier accepted a non-forced sibling deployment" >&2
  exit 1
fi

if DEPLOY_EVENT_NAME=push DEPLOY_EVENT_FORCED=true DEPLOY_EVENT_BEFORE="$before_sha" DEPLOY_EVENT_AFTER="$previous_sha" \
  "$ROOT_DIR/ci/verify-production-source-lineage.sh" "$fixture" "$previous_sha" "$current_sha" >/dev/null 2>&1; then
  echo "lineage verifier accepted a mismatched push after SHA" >&2
  exit 1
fi

git -C "$fixture" reset -q --hard "$base_sha"
wrong_subject_sha="$(commit_file wrong different-subject)"
if DEPLOY_EVENT_NAME=push DEPLOY_EVENT_FORCED=true DEPLOY_EVENT_BEFORE="$before_sha" DEPLOY_EVENT_AFTER="$wrong_subject_sha" \
  "$ROOT_DIR/ci/verify-production-source-lineage.sh" "$fixture" "$previous_sha" "$wrong_subject_sha" >/dev/null 2>&1; then
  echo "lineage verifier accepted an amended deployment with a different subject" >&2
  exit 1
fi

git -C "$fixture" reset -q --hard "$before_sha"
different_parent_sha="$(commit_file different-parent '[minor] Harden canonical IIIF processing and delivery')"
if DEPLOY_EVENT_NAME=push DEPLOY_EVENT_FORCED=true DEPLOY_EVENT_BEFORE="$before_sha" DEPLOY_EVENT_AFTER="$different_parent_sha" \
  "$ROOT_DIR/ci/verify-production-source-lineage.sh" "$fixture" "$previous_sha" "$different_parent_sha" >/dev/null 2>&1; then
  echo "lineage verifier accepted a deployment from a different commit slot" >&2
  exit 1
fi

echo "production source lineage contracts passed"
