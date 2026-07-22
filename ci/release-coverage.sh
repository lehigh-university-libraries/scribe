#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

fail() {
  echo "release coverage failed: $*" >&2
  exit 1
}

: "${RELEASE_SHA:?RELEASE_SHA is required}"
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
[[ "$RELEASE_SHA" =~ ^[0-9a-f]{40}$ ]] || fail "RELEASE_SHA must be a full commit SHA"
[ "$(git rev-parse HEAD)" = "$RELEASE_SHA" ] || fail "reviewed merge is not checked out"

covered=false
pending=false
exact_tags=()
while IFS= read -r tag; do
  [[ "$tag" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || continue
  tag_sha="$(git rev-parse "refs/tags/${tag}^{commit}")"
  [ "$tag_sha" = "$RELEASE_SHA" ] && exact_tags+=("$tag")
done < <(git tag --list)

if [ "${#exact_tags[@]}" -gt 1 ]; then
  fail "multiple numeric release tags point at the reviewed merge"
fi
if [ "${#exact_tags[@]}" -eq 1 ]; then
  exact_state="$(
    RELEASE_TAG="${exact_tags[0]}" RELEASE_SHA="$RELEASE_SHA" GITHUB_REPOSITORY="$GITHUB_REPOSITORY" \
      "$ROOT_DIR/ci/release-draft.sh" status
  )"
  if [ "$exact_state" = complete ]; then
    # The exact published release is already verified, so even tool downloads
    # are skipped regardless of a newer in-flight release.
    printf 'covered\n'
    exit 0
  fi
fi

while IFS= read -r tag; do
  [[ "$tag" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || continue
  tag_sha="$(git rev-parse "refs/tags/${tag}^{commit}")"
  [ "$tag_sha" != "$RELEASE_SHA" ] || continue
  git merge-base --is-ancestor "$RELEASE_SHA" "$tag_sha" || continue
  git merge-base --is-ancestor "$tag_sha" refs/remotes/origin/main || continue
  state="$(
    RELEASE_TAG="$tag" RELEASE_SHA="$tag_sha" GITHUB_REPOSITORY="$GITHUB_REPOSITORY" \
      "$ROOT_DIR/ci/release-draft.sh" status
  )"
  case "$state" in
    complete) covered=true ;;
    missing|draft) pending=true ;;
    *) fail "unexpected descendant release state: $state" ;;
  esac
done < <(git tag --list)

if [ "$covered" = true ]; then
  printf 'covered\n'
elif [ "$pending" = true ]; then
  fail "a later main tag exists but its exact release is not fully published"
else
  printf 'release\n'
fi
