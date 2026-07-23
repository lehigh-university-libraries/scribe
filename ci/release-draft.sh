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

release_list_endpoint="repos/${GITHUB_REPOSITORY}/releases?per_page=100"
release_assets_dir=""

cleanup() {
  if [ -n "$release_assets_dir" ] && [ -d "$release_assets_dir" ]; then
    find "$release_assets_dir" -depth -delete
  fi
}
trap cleanup EXIT

verify_remote_tag() {
  local ref_json remote_sha
  ref_json="$(gh api "repos/${GITHUB_REPOSITORY}/git/ref/tags/${RELEASE_TAG}")" ||
    fail "cannot read the remote release tag"
  remote_sha="$(jq -er '.object.sha' <<<"$ref_json")" || fail "remote release tag response is invalid"
  [ "$remote_sha" = "$RELEASE_SHA" ] || fail "remote release tag does not point at the reviewed release source"
}

verify_release() {
  local release_json="$1"
  jq -e \
    --arg tag "$RELEASE_TAG" '
      (.id | type == "number") and
      .tag_name == $tag and
      (.draft | type == "boolean") and
      .prerelease == false and
      (.assets | type == "array")
    ' <<<"$release_json" >/dev/null ||
    fail "release metadata must identify the exact reviewed tag as a non-prerelease"
}

verify_published_artifacts() {
  local release_json="$1"
  jq -e '
    any(.assets[]; .name == "checksums.txt") and
    any(.assets[]; (.name | test("^scribe_Linux_[A-Za-z0-9._-]+[.]tar[.]gz$"))) and
    any(.assets[]; (.name | test("^scribe_Darwin_[A-Za-z0-9._-]+[.]tar[.]gz$"))) and
    any(.assets[]; (.name | test("^scribe_Windows_[A-Za-z0-9._-]+[.]zip$")))
  ' <<<"$release_json" >/dev/null ||
    fail "release cannot be published without checksum and Linux, Darwin, and Windows archive assets"
}

verify_asset_checksums() {
  local actual_names checksum_names name
  command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is required to publish a release"
  release_assets_dir="$(mktemp -d)"
  gh release download "$RELEASE_TAG" \
    --repo "$GITHUB_REPOSITORY" \
    --dir "$release_assets_dir" >/dev/null ||
    fail "cannot download release assets for checksum verification"

  [ -f "$release_assets_dir/checksums.txt" ] ||
    fail "downloaded release assets do not contain checksums.txt"
  checksum_names="$(
    awk '
      NF != 2 { exit 1 }
      {
        name = $2
        sub(/^\*/, "", name)
        print name
      }
    ' "$release_assets_dir/checksums.txt" | LC_ALL=C sort
  )" || fail "checksums.txt is malformed"
  [ -n "$checksum_names" ] || fail "checksums.txt does not name any release archives"

  while IFS= read -r name; do
    [[ "$name" =~ ^scribe_((Darwin|Linux)_[A-Za-z0-9._-]+\.tar\.gz|Windows_[A-Za-z0-9._-]+\.zip)$ ]] ||
      fail "checksums.txt names an unexpected release asset"
  done <<<"$checksum_names"

  actual_names="$(
    find "$release_assets_dir" -maxdepth 1 -type f ! -name checksums.txt -printf '%f\n' |
      LC_ALL=C sort
  )"
  [ "$actual_names" = "$checksum_names" ] ||
    fail "checksums.txt does not cover exactly the uploaded release archives"
  (
    cd "$release_assets_dir"
    sha256sum --check --quiet --strict checksums.txt
  ) || fail "release asset checksum verification failed"
}

get_release() {
  local list_pages matches match_count

  if ! list_pages="$(gh api --paginate "$release_list_endpoint")"; then
    echo "release draft failed: cannot list repository releases" >&2
    return 2
  fi
  if ! matches="$(
    jq -sce --arg tag "$RELEASE_TAG" '
      if all(.[]; type == "array") then
        [.[] | .[] | select(type == "object" and .tag_name == $tag)]
      else
        error("release listing response must contain arrays")
      end
    ' <<<"$list_pages"
  )"; then
    echo "release draft failed: repository release listing is invalid" >&2
    return 2
  fi
  match_count="$(jq -r 'length' <<<"$matches")"
  case "$match_count" in
    0) return 1 ;;
    1) jq -c '.[0]' <<<"$matches" ;;
    *)
      echo "release draft failed: multiple releases use tag $RELEASE_TAG" >&2
      return 2
      ;;
  esac
}

status() {
  local lookup_status release_json
  verify_remote_tag
  if release_json="$(get_release)"; then
    :
  else
    lookup_status=$?
    if [ "$lookup_status" -eq 1 ]; then
      printf 'missing\n'
      return
    fi
    fail "cannot discover the release"
  fi
  verify_release "$release_json"
  if jq -e '.draft == true' <<<"$release_json" >/dev/null; then
    printf 'draft\n'
    return
  fi
  verify_published_artifacts "$release_json"
  verify_asset_checksums
  printf 'complete\n'
}

prepare() {
  local create_status lookup_status release_json
  verify_remote_tag
  if release_json="$(get_release)"; then
    :
  else
    lookup_status=$?
    [ "$lookup_status" -eq 1 ] || fail "cannot discover the release draft"
    # A failed create may mean a concurrent/previous request reached GitHub but
    # its response did not reach the runner. Re-read and verify either way.
    if release_json="$(
      gh api --method POST "repos/${GITHUB_REPOSITORY}/releases" \
        --field tag_name="$RELEASE_TAG" \
        --field target_commitish="$RELEASE_SHA" \
        --field name="$RELEASE_TAG" \
        --field draft=true \
        --field prerelease=false \
        --field generate_release_notes=true
    )"; then
      :
    else
      create_status=$?
      release_json="$(get_release)" ||
        fail "cannot create or recover the release draft (create exit $create_status)"
    fi
  fi
  verify_release "$release_json"
  if jq -e '.draft == false' <<<"$release_json" >/dev/null; then
    verify_published_artifacts "$release_json"
    verify_asset_checksums
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
  verify_asset_checksums
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
