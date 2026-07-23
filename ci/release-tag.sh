#!/usr/bin/env bash

set -euo pipefail

fail() {
  echo "release tag failed: $*" >&2
  exit 1
}

: "${RELEASE_SHA:?RELEASE_SHA is required}"
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
autotag_branch="${AUTOTAG_BRANCH:-release-candidate}"
expected_release_tag="${EXPECTED_RELEASE_TAG:-}"
[[ "$RELEASE_SHA" =~ ^[0-9a-f]{40}$ ]] || fail "RELEASE_SHA must be a full commit SHA"
[[ "$GITHUB_REPOSITORY" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || fail "GITHUB_REPOSITORY must be an owner/repository slug"
[[ "$autotag_branch" =~ ^[A-Za-z0-9][A-Za-z0-9._/-]{0,63}$ ]] || fail "AUTOTAG_BRANCH is invalid"
if [ -n "$expected_release_tag" ]; then
  [[ "$expected_release_tag" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
    fail "EXPECTED_RELEASE_TAG must be numeric SemVer"
fi
command -v git >/dev/null 2>&1 || fail "git is required"
command -v gh >/dev/null 2>&1 || fail "gh is required"

head_sha="$(git rev-parse HEAD)"
[ "$head_sha" = "$RELEASE_SHA" ] || fail "reviewed release SHA is not the checked-out release commit"

numeric_tags=()
while IFS= read -r tag; do
  [[ "$tag" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] && numeric_tags+=("$tag")
done < <(git tag --points-at "$RELEASE_SHA" --list)

case "${#numeric_tags[@]}" in
  0)
    : "${AUTOTAG_BIN:?AUTOTAG_BIN is required when this commit has no release tag}"
    [ -x "$AUTOTAG_BIN" ] || fail "AUTOTAG_BIN must be executable"
    tag="$($AUTOTAG_BIN --empty-version-prefix --branch "$autotag_branch")"
    [[ "$tag" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "AutoTag returned a non-numeric SemVer tag"
    if [ -n "$expected_release_tag" ] && [ "$tag" != "$expected_release_tag" ]; then
      fail "AutoTag created $tag instead of expected release tag $expected_release_tag"
    fi
    [ "$(git rev-parse "refs/tags/${tag}^{commit}")" = "$RELEASE_SHA" ] ||
      fail "AutoTag did not create the tag on the reviewed main commit"
    gh api --method POST "repos/${GITHUB_REPOSITORY}/git/refs" \
      --field ref="refs/tags/$tag" \
      --field sha="$RELEASE_SHA" >/dev/null
    ;;
  1)
    tag="${numeric_tags[0]}"
    if [ -n "$expected_release_tag" ] && [ "$tag" != "$expected_release_tag" ]; then
      fail "existing release tag $tag does not match expected release tag $expected_release_tag"
    fi
    [ "$(git rev-parse "refs/tags/${tag}^{commit}")" = "$RELEASE_SHA" ] ||
      fail "existing release tag does not resolve to the reviewed main commit"
    ;;
  *)
    fail "multiple numeric release tags point at the reviewed main commit"
    ;;
esac

remote_sha="$(gh api "repos/${GITHUB_REPOSITORY}/git/ref/tags/$tag" --jq '.object.sha')" ||
  fail "cannot verify the remote release tag"
[ "$remote_sha" = "$RELEASE_SHA" ] || fail "remote release tag does not point at the reviewed main commit"

printf '%s\n' "$tag"
