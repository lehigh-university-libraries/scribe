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
[ "${1:-}" = "api" ]
shift
method="GET"
if [ "${1:-}" = "--method" ]; then
  method="$2"
  shift 2
fi
endpoint="${1:-}"
shift || true

case "$method:$endpoint" in
  GET:repos/*/git/ref/tags/*)
    jq -n --arg sha "$(cat "$FAKE_REMOTE_SHA")" '{object: {sha: $sha}}'
    ;;
  GET:repos/*/releases/tags/*)
    [ -s "$FAKE_RELEASE_JSON" ] || exit 1
    cat "$FAKE_RELEASE_JSON"
    ;;
  POST:repos/*/releases)
    tag=""
    sha=""
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --field)
          case "$2" in
            tag_name=*) tag="${2#tag_name=}" ;;
            target_commitish=*) sha="${2#target_commitish=}" ;;
          esac
          shift 2
          ;;
        *) shift ;;
      esac
    done
    [ "$tag" = "$FAKE_RELEASE_TAG" ]
    [ "$sha" = "$(cat "$FAKE_REMOTE_SHA")" ]
    jq -n --arg tag "$tag" --arg sha "$sha" \
      '{id: 42, tag_name: $tag, target_commitish: $sha, draft: true, assets: []}' >"$FAKE_RELEASE_JSON"
    count="$(cat "$FAKE_CREATE_COUNT")"
    printf '%s\n' "$((count + 1))" >"$FAKE_CREATE_COUNT"
    ;;
  PATCH:repos/*/releases/42)
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

printf '%s\n' "$EXPECTED_SHA" >"$TEST_DIR/remote-sha"
: >"$TEST_DIR/release.json"
printf '0\n' >"$TEST_DIR/create-count"
printf '0\n' >"$TEST_DIR/publish-count"

run_draft() {
  PATH="$TEST_DIR/bin:$PATH" \
    FAKE_CREATE_COUNT="$TEST_DIR/create-count" \
    FAKE_PUBLISH_COUNT="$TEST_DIR/publish-count" \
    FAKE_RELEASE_JSON="$TEST_DIR/release.json" \
    FAKE_RELEASE_TAG="$EXPECTED_RELEASE_TAG" \
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
[ "$(run_draft status)" = "draft" ]
[ "$(run_draft prepare)" = "publish" ]
[ "$(cat "$TEST_DIR/create-count")" = "1" ]

jq '.assets = [{name: "checksums.txt"}, {name: "scribe_Linux_x86_64.tar.gz"}]' \
  "$TEST_DIR/release.json" >"$TEST_DIR/release.tmp"
mv "$TEST_DIR/release.tmp" "$TEST_DIR/release.json"
[ "$(run_draft publish)" = "complete" ]
[ "$(cat "$TEST_DIR/publish-count")" = "1" ]

# A fully published exact release is a no-op: no new draft and no republish.
[ "$(run_draft prepare)" = "complete" ]
[ "$(run_draft status)" = "complete" ]
[ "$(cat "$TEST_DIR/create-count")" = "1" ]
[ "$(cat "$TEST_DIR/publish-count")" = "1" ]

printf '%s\n' 'fedcba9876543210fedcba9876543210fedcba98' >"$TEST_DIR/remote-sha"
if run_draft prepare >"$TEST_DIR/moved.out" 2>"$TEST_DIR/moved.err"; then
  echo "release draft accepted a moved tag" >&2
  exit 1
fi
grep -F 'remote release tag does not point at the reviewed merge' "$TEST_DIR/moved.err" >/dev/null

printf '%s\n' "$EXPECTED_SHA" >"$TEST_DIR/remote-sha"
jq '.target_commitish = "fedcba9876543210fedcba9876543210fedcba98"' \
  "$TEST_DIR/release.json" >"$TEST_DIR/release.tmp"
mv "$TEST_DIR/release.tmp" "$TEST_DIR/release.json"
if run_draft prepare >"$TEST_DIR/target.out" 2>"$TEST_DIR/target.err"; then
  echo "release draft accepted another target commit" >&2
  exit 1
fi
grep -F 'release must target the exact reviewed tag and merge SHA' "$TEST_DIR/target.err" >/dev/null

echo "Release draft creation, retry, exact-target verification, and final publication are idempotent."
