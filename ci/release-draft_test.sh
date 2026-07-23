#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d)"
trap 'rm -rf "$TEST_DIR"' EXIT
mkdir -p "$TEST_DIR/bin"

readonly EXPECTED_RELEASE_TAG="1.2.3"
readonly EXPECTED_SHA="0123456789abcdef0123456789abcdef01234567"
readonly REPOSITORY="lehigh-university-libraries/scribe"

cat >"$TEST_DIR/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = "release" ] && [ "${2:-}" = "download" ]; then
  [ "${3:-}" = "$FAKE_RELEASE_TAG" ]
  shift 3
  repository=""
  destination=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --repo) repository="$2"; shift 2 ;;
      --dir) destination="$2"; shift 2 ;;
      *) echo "unexpected fake gh release download argument: $1" >&2; exit 2 ;;
    esac
  done
  [ "$repository" = "$FAKE_REPOSITORY" ]
  [ -d "$destination" ]
  cp -a "$FAKE_ASSET_SOURCE/." "$destination/"
  count="$(cat "$FAKE_DOWNLOAD_COUNT")"
  printf '%s\n' "$((count + 1))" >"$FAKE_DOWNLOAD_COUNT"
  exit 0
fi
[ "${1:-}" = "api" ]
shift
method="GET"
paginate=false
endpoint=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --method)
      method="$2"
      shift 2
      ;;
    --paginate)
      paginate=true
      shift
      ;;
    repos/*)
      endpoint="$1"
      shift
      break
      ;;
    *)
      echo "unexpected fake gh api argument before endpoint: $1" >&2
      exit 2
      ;;
  esac
done

case "$method:$endpoint" in
  GET:repos/*/git/ref/tags/*)
    [ "$paginate" = false ]
    jq -n --arg sha "$(cat "$FAKE_REMOTE_SHA")" '{object: {sha: $sha}}'
    ;;
  GET:repos/*/releases\?per_page=100)
    [ "$paginate" = true ]
    # The requested draft is deliberately on a later page so the contract
    # exercises authenticated pagination rather than only the first response.
    jq -n '[{
      id: 7,
      tag_name: "9.9.9",
      target_commitish: "main",
      draft: false,
      prerelease: false,
      assets: []
    }]'
    if [ ! -s "$FAKE_RELEASE_JSON" ]; then
      printf '[]\n'
    elif [ "$FAKE_DUPLICATE_RELEASES" = true ]; then
      # `gh api --paginate` writes each response page as a separate JSON value.
      jq -s '.' "$FAKE_RELEASE_JSON"
      jq -s '.' "$FAKE_RELEASE_JSON"
    else
      jq -s '.' "$FAKE_RELEASE_JSON"
    fi
    ;;
  POST:repos/*/releases)
    [ "$paginate" = false ]
    tag=""
    sha=""
    prerelease_field=false
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --field)
          case "$2" in
            tag_name=*) tag="${2#tag_name=}" ;;
            target_commitish=*) sha="${2#target_commitish=}" ;;
            prerelease=false) prerelease_field=true ;;
          esac
          shift 2
          ;;
        *) shift ;;
      esac
    done
    [ "$tag" = "$FAKE_RELEASE_TAG" ]
    [ "$sha" = "$(cat "$FAKE_REMOTE_SHA")" ]
    [ "$prerelease_field" = true ]
    # GitHub documents target_commitish as unused when the tag already exists;
    # real release responses commonly retain the default branch here.
    jq -n --arg tag "$tag" \
      '{id: 42, tag_name: $tag, target_commitish: "main", draft: true, prerelease: false, assets: []}' >"$FAKE_RELEASE_JSON"
    count="$(cat "$FAKE_CREATE_COUNT")"
    printf '%s\n' "$((count + 1))" >"$FAKE_CREATE_COUNT"
    cat "$FAKE_RELEASE_JSON"
    ;;
  PATCH:repos/*/releases/42)
    [ "$paginate" = false ]
    draft_field=false
    latest_field=false
    while [ "$#" -gt 0 ]; do
      case "$1:$2" in
        --field:draft=false) draft_field=true; shift 2 ;;
        --raw-field:make_latest=true) latest_field=true; shift 2 ;;
        *) shift ;;
      esac
    done
    [ "$draft_field" = true ]
    [ "$latest_field" = true ]
    jq '.draft = false' "$FAKE_RELEASE_JSON" >"$FAKE_RELEASE_TMP"
    mv "$FAKE_RELEASE_TMP" "$FAKE_RELEASE_JSON"
    count="$(cat "$FAKE_PUBLISH_COUNT")"
    printf '%s\n' "$((count + 1))" >"$FAKE_PUBLISH_COUNT"
    ;;
  *)
    echo "unexpected fake gh invocation: $method $endpoint $*" >&2
    exit 2
    ;;
esac
EOF
chmod +x "$TEST_DIR/bin/gh"

mkdir "$TEST_DIR/assets"
printf 'linux archive\n' >"$TEST_DIR/assets/scribe_Linux_x86_64.tar.gz"
printf 'darwin archive\n' >"$TEST_DIR/assets/scribe_Darwin_arm64.tar.gz"
printf 'windows archive\n' >"$TEST_DIR/assets/scribe_Windows_x86_64.zip"
(
  cd "$TEST_DIR/assets"
  sha256sum scribe_* >checksums.txt
)
printf '%s\n' "$EXPECTED_SHA" >"$TEST_DIR/remote-sha"
: >"$TEST_DIR/release.json"
printf '0\n' >"$TEST_DIR/create-count"
printf '0\n' >"$TEST_DIR/download-count"
printf '0\n' >"$TEST_DIR/publish-count"

run_draft() {
  PATH="$TEST_DIR/bin:$PATH" \
    FAKE_ASSET_SOURCE="$TEST_DIR/assets" \
    FAKE_CREATE_COUNT="$TEST_DIR/create-count" \
    FAKE_DOWNLOAD_COUNT="$TEST_DIR/download-count" \
    FAKE_DUPLICATE_RELEASES="${FAKE_DUPLICATE_RELEASES:-false}" \
    FAKE_PUBLISH_COUNT="$TEST_DIR/publish-count" \
    FAKE_RELEASE_JSON="$TEST_DIR/release.json" \
    FAKE_RELEASE_TAG="$EXPECTED_RELEASE_TAG" \
    FAKE_REPOSITORY="$REPOSITORY" \
    FAKE_RELEASE_TMP="$TEST_DIR/release.tmp" \
    FAKE_REMOTE_SHA="$TEST_DIR/remote-sha" \
    GITHUB_REPOSITORY="$REPOSITORY" \
    RELEASE_SHA="$EXPECTED_SHA" \
    RELEASE_TAG="$EXPECTED_RELEASE_TAG" \
    "$ROOT_DIR/ci/release-draft.sh" "$1"
}

[ "$(run_draft status)" = "missing" ]
[ "$(run_draft prepare)" = "publish" ]
[ "$(cat "$TEST_DIR/create-count")" = "1" ]
[ "$(jq -r '.target_commitish' "$TEST_DIR/release.json")" = "main" ]
[ "$(run_draft status)" = "draft" ]
[ "$(run_draft prepare)" = "publish" ]
[ "$(cat "$TEST_DIR/create-count")" = "1" ]

jq '.prerelease = true' \
  "$TEST_DIR/release.json" >"$TEST_DIR/release.tmp"
mv "$TEST_DIR/release.tmp" "$TEST_DIR/release.json"
if run_draft prepare >"$TEST_DIR/draft-prerelease.out" 2>"$TEST_DIR/draft-prerelease.err"; then
  echo "release preparation accepted a numeric prerelease draft" >&2
  exit 1
fi
grep -F 'release metadata must identify the exact reviewed tag as a non-prerelease' \
  "$TEST_DIR/draft-prerelease.err" >/dev/null
jq '.prerelease = false' \
  "$TEST_DIR/release.json" >"$TEST_DIR/release.tmp"
mv "$TEST_DIR/release.tmp" "$TEST_DIR/release.json"

jq '.assets = [{name: "checksums.txt"}, {name: "notes.txt"}]' \
  "$TEST_DIR/release.json" >"$TEST_DIR/release.tmp"
mv "$TEST_DIR/release.tmp" "$TEST_DIR/release.json"
if run_draft publish >"$TEST_DIR/assets.out" 2>"$TEST_DIR/assets.err"; then
  echo "release draft accepted an arbitrary non-archive asset" >&2
  exit 1
fi
grep -F 'release cannot be published without checksum and Linux, Darwin, and Windows archive assets' \
  "$TEST_DIR/assets.err" >/dev/null
[ "$(cat "$TEST_DIR/download-count")" = "0" ]
[ "$(cat "$TEST_DIR/publish-count")" = "0" ]

jq '.assets = [
  {name: "checksums.txt"},
  {name: "scribe_Linux_x86_64.tar.gz"},
  {name: "scribe_Darwin_arm64.tar.gz"},
  {name: "scribe_Windows_x86_64.zip"}
]' "$TEST_DIR/release.json" >"$TEST_DIR/release.tmp"
mv "$TEST_DIR/release.tmp" "$TEST_DIR/release.json"
printf 'tampered\n' >>"$TEST_DIR/assets/scribe_Linux_x86_64.tar.gz"
if run_draft publish >"$TEST_DIR/checksum.out" 2>"$TEST_DIR/checksum.err"; then
  echo "release draft accepted an archive that did not match checksums.txt" >&2
  exit 1
fi
grep -F 'release asset checksum verification failed' "$TEST_DIR/checksum.err" >/dev/null
[ "$(cat "$TEST_DIR/download-count")" = "1" ]
[ "$(cat "$TEST_DIR/publish-count")" = "0" ]

(
  cd "$TEST_DIR/assets"
  sha256sum scribe_* >checksums.txt
)
[ "$(run_draft publish)" = "complete" ]
[ "$(cat "$TEST_DIR/publish-count")" = "1" ]

# A published release must still pass byte-for-byte asset verification before it
# can make a retry a no-op.
printf 'tampered after publication\n' >>"$TEST_DIR/assets/scribe_Linux_x86_64.tar.gz"
if run_draft status >"$TEST_DIR/published-checksum.out" 2>"$TEST_DIR/published-checksum.err"; then
  echo "release status accepted a published archive that did not match checksums.txt" >&2
  exit 1
fi
grep -F 'release asset checksum verification failed' "$TEST_DIR/published-checksum.err" >/dev/null
printf 'linux archive\n' >"$TEST_DIR/assets/scribe_Linux_x86_64.tar.gz"
(
  cd "$TEST_DIR/assets"
  sha256sum scribe_* >checksums.txt
)

# A fully verified published exact release is a no-op: no new draft and no
# republish.
[ "$(run_draft prepare)" = "complete" ]
[ "$(run_draft status)" = "complete" ]
[ "$(cat "$TEST_DIR/create-count")" = "1" ]
[ "$(cat "$TEST_DIR/download-count")" = "5" ]
[ "$(cat "$TEST_DIR/publish-count")" = "1" ]

printf '%s\n' 'fedcba9876543210fedcba9876543210fedcba98' >"$TEST_DIR/remote-sha"
if run_draft prepare >"$TEST_DIR/moved.out" 2>"$TEST_DIR/moved.err"; then
  echo "release draft accepted a moved tag" >&2
  exit 1
fi
grep -F 'remote release tag does not point at the reviewed release source' "$TEST_DIR/moved.err" >/dev/null

printf '%s\n' "$EXPECTED_SHA" >"$TEST_DIR/remote-sha"
jq '.target_commitish = "another-default-branch"' \
  "$TEST_DIR/release.json" >"$TEST_DIR/release.tmp"
mv "$TEST_DIR/release.tmp" "$TEST_DIR/release.json"
[ "$(run_draft status)" = "complete" ]

jq '.prerelease = true' \
  "$TEST_DIR/release.json" >"$TEST_DIR/release.tmp"
mv "$TEST_DIR/release.tmp" "$TEST_DIR/release.json"
if run_draft status >"$TEST_DIR/prerelease.out" 2>"$TEST_DIR/prerelease.err"; then
  echo "release status accepted a numeric prerelease" >&2
  exit 1
fi
grep -F 'release metadata must identify the exact reviewed tag as a non-prerelease' \
  "$TEST_DIR/prerelease.err" >/dev/null
jq '.prerelease = false' \
  "$TEST_DIR/release.json" >"$TEST_DIR/release.tmp"
mv "$TEST_DIR/release.tmp" "$TEST_DIR/release.json"

if FAKE_DUPLICATE_RELEASES=true run_draft status \
  >"$TEST_DIR/duplicate.out" 2>"$TEST_DIR/duplicate.err"; then
  echo "release draft accepted duplicate exact-tag releases across pages" >&2
  exit 1
fi
grep -F "multiple releases use tag $EXPECTED_RELEASE_TAG" "$TEST_DIR/duplicate.err" >/dev/null

echo "Release publication discovers drafts by listing, trusts the verified tag ref, rejects duplicates, and verifies assets."
