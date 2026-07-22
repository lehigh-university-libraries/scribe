#!/usr/bin/env bash

set -euo pipefail

fail() {
  echo "release draft failed: $*" >&2
  exit 1
}

: "${RELEASE_TAG:?RELEASE_TAG is required}"
: "${RELEASE_SHA:?RELEASE_SHA is required}"
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
[[ "$RELEASE_TAG" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "RELEASE_TAG must be numeric SemVer"
[[ "$RELEASE_SHA" =~ ^[0-9a-f]{40}$ ]] || fail "RELEASE_SHA must be a full commit SHA"
[[ "$GITHUB_REPOSITORY" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || fail "GITHUB_REPOSITORY must be an owner/repository slug"
command -v gh >/dev/null 2>&1 || fail "gh is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"

release_endpoint="repos/${GITHUB_REPOSITORY}/releases/tags/${RELEASE_TAG}"

verify_remote_tag() {
  local ref_json remote_sha
  ref_json="$(gh api "repos/${GITHUB_REPOSITORY}/git/ref/tags/${RELEASE_TAG}")" ||
    fail "cannot read the remote release tag"
  remote_sha="$(jq -er '.object.sha' <<<"$ref_json")" || fail "remote release tag response is invalid"
  [ "$remote_sha" = "$RELEASE_SHA" ] || fail "remote release tag does not point at the reviewed merge"
}

verify_release() {
  local release_json="$1"
  jq -e \
    --arg tag "$RELEASE_TAG" \
    --arg sha "$RELEASE_SHA" '
      (.id | type == "number") and
      .tag_name == $tag and
      .target_commitish == $sha and
      (.draft | type == "boolean") and
      (.assets | type == "array")
    ' <<<"$release_json" >/dev/null ||
    fail "release must target the exact reviewed tag and merge SHA"
}

verify_published_artifacts() {
  local release_json="$1"
  jq -e '
    (.assets | length) >= 2 and
    any(.assets[]; .name == "checksums.txt")
  ' <<<"$release_json" >/dev/null ||
    fail "release cannot be published without checksum and archive assets"
}

get_release() {
  gh api "$release_endpoint"
}

status() {
  local release_json
  verify_remote_tag
  if ! release_json="$(get_release 2>/dev/null)"; then
    printf 'missing\n'
    return
  fi
  verify_release "$release_json"
  if jq -e '.draft == true' <<<"$release_json" >/dev/null; then
    printf 'draft\n'
    return
  fi
  verify_published_artifacts "$release_json"
  printf 'complete\n'
}

prepare() {
  local release_json
  verify_remote_tag
  if ! release_json="$(get_release 2>/dev/null)"; then
    # A failed create may mean a concurrent/previous request reached GitHub but
    # its response did not reach the runner. Re-read and verify either way.
    gh api --method POST "repos/${GITHUB_REPOSITORY}/releases" \
      --field tag_name="$RELEASE_TAG" \
      --field target_commitish="$RELEASE_SHA" \
      --field name="$RELEASE_TAG" \
      --field draft=true \
      --field generate_release_notes=true >/dev/null 2>&1 || true
    release_json="$(get_release)" || fail "cannot create or read the release draft"
  fi
  verify_release "$release_json"
  if jq -e '.draft == false' <<<"$release_json" >/dev/null; then
    verify_published_artifacts "$release_json"
    printf 'complete\n'
    return
  fi
  printf 'publish\n'
}

publish() {
  local release_json release_id published_json
  verify_remote_tag
  release_json="$(get_release)" || fail "cannot read the release draft"
  verify_release "$release_json"
  verify_published_artifacts "$release_json"
  if jq -e '.draft == false' <<<"$release_json" >/dev/null; then
    printf 'complete\n'
    return
  fi
  release_id="$(jq -er '.id' <<<"$release_json")"
  gh api --method PATCH "repos/${GITHUB_REPOSITORY}/releases/${release_id}" \
    --field draft=false \
    --raw-field make_latest=true >/dev/null
  published_json="$(get_release)" || fail "cannot verify the published release"
  verify_release "$published_json"
  verify_published_artifacts "$published_json"
  jq -e '.draft == false' <<<"$published_json" >/dev/null || fail "release remained a draft"
  printf 'complete\n'
}

case "${1:-}" in
  status) status ;;
  prepare) prepare ;;
  publish) publish ;;
  *) fail "usage: release-draft.sh status|prepare|publish" ;;
esac
