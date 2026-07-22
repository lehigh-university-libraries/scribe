#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d)"
trap 'rm -rf "$TEST_DIR"' EXIT
mkdir -p "$TEST_DIR/bin"

readonly MERGE_SHA="0123456789abcdef0123456789abcdef01234567"
readonly LATER_SHA="1111111111111111111111111111111111111111"
readonly REPOSITORY="lehigh-university-libraries/scribe"

cat >"$TEST_DIR/bin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "${1:-}:${2:-}" in
  rev-parse:HEAD) printf '%s\n' "$FAKE_MERGE_SHA" ;;
  rev-parse:refs/tags/*)
    tag="${2#refs/tags/}"
    tag="${tag%\^\{commit\}}"
    if [ "$tag" = "1.2.3" ]; then printf '%s\n' "$FAKE_MERGE_SHA"; else printf '%s\n' "$FAKE_LATER_SHA"; fi
    ;;
  tag:--list) [ ! -s "$FAKE_TAGS" ] || cat "$FAKE_TAGS" ;;
  merge-base:--is-ancestor)
    if [ "$3" = "$FAKE_MERGE_SHA" ] && [ "$4" = "$FAKE_LATER_SHA" ]; then exit 0; fi
    if [ "$3" = "$FAKE_LATER_SHA" ] && [ "$4" = "refs/remotes/origin/main" ]; then exit 0; fi
    exit 1
    ;;
  *) echo "unexpected fake git invocation: $*" >&2; exit 2 ;;
esac
EOF

cat >"$TEST_DIR/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[ "${1:-}" = "api" ]
endpoint="${2:-}"
case "$endpoint" in
  repos/*/git/ref/tags/*)
    tag="${endpoint##*/}"
    if [ "$tag" = "1.2.3" ]; then sha="$(cat "$FAKE_EXACT_REMOTE_SHA")"; else sha="$(cat "$FAKE_REMOTE_SHA")"; fi
    jq -n --arg sha "$sha" '{object: {sha: $sha}}'
    ;;
  repos/*/releases/tags/*)
    tag="${endpoint##*/}"
    if [ "$tag" = "1.2.3" ]; then release="$FAKE_EXACT_RELEASE_JSON"; else release="$FAKE_RELEASE_JSON"; fi
    [ -s "$release" ] || exit 1
    cat "$release"
    ;;
  *) echo "unexpected fake gh invocation: $*" >&2; exit 2 ;;
esac
EOF
chmod +x "$TEST_DIR/bin/"*
: >"$TEST_DIR/tags"
printf '%s\n' "$LATER_SHA" >"$TEST_DIR/remote-sha"
printf '%s\n' "$MERGE_SHA" >"$TEST_DIR/exact-remote-sha"
: >"$TEST_DIR/release.json"
: >"$TEST_DIR/exact-release.json"

run_coverage() {
  PATH="$TEST_DIR/bin:$PATH" \
    FAKE_LATER_SHA="$LATER_SHA" \
    FAKE_MERGE_SHA="$MERGE_SHA" \
    FAKE_EXACT_RELEASE_JSON="$TEST_DIR/exact-release.json" \
    FAKE_EXACT_REMOTE_SHA="$TEST_DIR/exact-remote-sha" \
    FAKE_RELEASE_JSON="$TEST_DIR/release.json" \
    FAKE_REMOTE_SHA="$TEST_DIR/remote-sha" \
    FAKE_TAGS="$TEST_DIR/tags" \
    GITHUB_REPOSITORY="$REPOSITORY" \
    RELEASE_SHA="$MERGE_SHA" \
    "$ROOT_DIR/ci/release-coverage.sh"
}

[ "$(run_coverage)" = "release" ]

printf '1.2.4\n' >"$TEST_DIR/tags"
jq -n --arg tag "1.2.4" --arg sha "$LATER_SHA" '
  {id: 42, tag_name: $tag, target_commitish: $sha, draft: true, assets: []}
' >"$TEST_DIR/release.json"
if run_coverage >"$TEST_DIR/pending.out" 2>"$TEST_DIR/pending.err"; then
  echo "release coverage accepted an unfinished later release" >&2
  exit 1
fi
grep -F 'later main tag exists but its exact release is not fully published' "$TEST_DIR/pending.err" >/dev/null

jq '.draft = false | .assets = [{name: "checksums.txt"}, {name: "scribe_Linux_x86_64.tar.gz"}]' \
  "$TEST_DIR/release.json" >"$TEST_DIR/release.tmp"
mv "$TEST_DIR/release.tmp" "$TEST_DIR/release.json"
[ "$(run_coverage)" = "covered" ]

printf '%s\n' 'fedcba9876543210fedcba9876543210fedcba98' >"$TEST_DIR/remote-sha"
if run_coverage >"$TEST_DIR/moved.out" 2>"$TEST_DIR/moved.err"; then
  echo "release coverage accepted a moved descendant tag" >&2
  exit 1
fi
grep -F 'remote release tag does not point at the reviewed merge' "$TEST_DIR/moved.err" >/dev/null

# A completed exact release remains an idempotent no-op even while a newer
# merge's release is still a draft and its event arrived first.
printf '%s\n' "$LATER_SHA" >"$TEST_DIR/remote-sha"
printf '1.2.3\n1.2.4\n' >"$TEST_DIR/tags"
jq -n --arg tag "1.2.3" --arg sha "$MERGE_SHA" '
  {id: 41, tag_name: $tag, target_commitish: $sha, draft: false,
   assets: [{name: "checksums.txt"}, {name: "scribe_Linux_x86_64.tar.gz"}]}
' >"$TEST_DIR/exact-release.json"
jq -n --arg tag "1.2.4" --arg sha "$LATER_SHA" '
  {id: 42, tag_name: $tag, target_commitish: $sha, draft: true, assets: []}
' >"$TEST_DIR/release.json"
[ "$(run_coverage)" = "covered" ]

echo "Out-of-order merge events no-op only after a later exact release is fully published."
