#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d)"
trap 'rm -rf "$TEST_DIR"' EXIT
mkdir -p "$TEST_DIR/bin"

readonly EXPECTED_RELEASE_SHA="0123456789abcdef0123456789abcdef01234567"
readonly REPOSITORY="lehigh-university-libraries/scribe"

cat >"$TEST_DIR/bin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "${1:-}:${2:-}" in
  rev-parse:HEAD)
    printf '%s\n' "$FAKE_HEAD_SHA"
    ;;
  rev-parse:refs/tags/*)
    tag="${2#refs/tags/}"
    tag="${tag%\^\{commit\}}"
    [ -s "$FAKE_LOCAL_TAG" ] && [ "$(cat "$FAKE_LOCAL_TAG")" = "$tag" ]
    printf '%s\n' "$FAKE_RELEASE_SHA"
    ;;
  tag:--points-at)
    [ "${3:-}" = "$FAKE_RELEASE_SHA" ]
    [ "${4:-}" = "--list" ]
    [ ! -s "$FAKE_LOCAL_TAG" ] || cat "$FAKE_LOCAL_TAG"
    ;;
  *)
    echo "unexpected fake git invocation: $*" >&2
    exit 2
    ;;
esac
EOF

cat >"$TEST_DIR/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" != "api" ]; then
  echo "unexpected fake gh invocation: $*" >&2
  exit 2
fi
shift
if [ "${1:-}" = "--method" ] && [ "${2:-}" = "POST" ]; then
  shift 2
  endpoint="$1"
  shift
  ref=""
  sha=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --field)
        case "$2" in
          ref=*) ref="${2#ref=}" ;;
          sha=*) sha="${2#sha=}" ;;
        esac
        shift 2
        ;;
      *) shift ;;
    esac
  done
  [ "$endpoint" = "repos/${FAKE_REPOSITORY}/git/refs" ]
  [ "$ref" = "refs/tags/$(cat "$FAKE_LOCAL_TAG")" ]
  [ "$sha" = "$FAKE_RELEASE_SHA" ]
  printf '%s\n' "$sha" >"$FAKE_REMOTE_SHA"
  count="$(cat "$FAKE_POST_COUNT")"
  printf '%s\n' "$((count + 1))" >"$FAKE_POST_COUNT"
  printf '{}\n'
  exit 0
fi

endpoint="${1:-}"
[ "$endpoint" = "repos/${FAKE_REPOSITORY}/git/ref/tags/$(cat "$FAKE_LOCAL_TAG")" ]
[ -s "$FAKE_REMOTE_SHA" ]
cat "$FAKE_REMOTE_SHA"
EOF

cat >"$TEST_DIR/bin/autotag" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[ "$*" = "--empty-version-prefix --branch release-candidate" ]
count="$(cat "$FAKE_AUTOTAG_COUNT")"
printf '%s\n' "$((count + 1))" >"$FAKE_AUTOTAG_COUNT"
printf '%s\n' "1.2.3" >"$FAKE_LOCAL_TAG"
printf '%s\n' "1.2.3"
EOF

chmod +x "$TEST_DIR/bin/"*
printf '0\n' >"$TEST_DIR/autotag-count"
printf '0\n' >"$TEST_DIR/post-count"
: >"$TEST_DIR/local-tag"
: >"$TEST_DIR/remote-sha"

run_release_tag() {
  PATH="$TEST_DIR/bin:$PATH" \
    AUTOTAG_BIN="$TEST_DIR/bin/autotag" \
    AUTOTAG_BRANCH=release-candidate \
    FAKE_AUTOTAG_COUNT="$TEST_DIR/autotag-count" \
    FAKE_HEAD_SHA="${FAKE_HEAD_SHA:-$EXPECTED_RELEASE_SHA}" \
    FAKE_LOCAL_TAG="$TEST_DIR/local-tag" \
    FAKE_POST_COUNT="$TEST_DIR/post-count" \
    FAKE_RELEASE_SHA="$EXPECTED_RELEASE_SHA" \
    FAKE_REMOTE_SHA="$TEST_DIR/remote-sha" \
    FAKE_REPOSITORY="$REPOSITORY" \
    EXPECTED_RELEASE_TAG="${EXPECTED_RELEASE_TAG:-1.2.3}" \
    GITHUB_REPOSITORY="$REPOSITORY" \
    RELEASE_SHA="$EXPECTED_RELEASE_SHA" \
    "$ROOT_DIR/ci/release-tag.sh"
}

first_tag="$(run_release_tag)"
[ "$first_tag" = "1.2.3" ]
[ "$(cat "$TEST_DIR/autotag-count")" = "1" ]
[ "$(cat "$TEST_DIR/post-count")" = "1" ]
[ "$(cat "$TEST_DIR/remote-sha")" = "$EXPECTED_RELEASE_SHA" ]

# Simulate a later GoReleaser or Homebrew failure. A workflow rerun starts from
# the remote tag created above and must not calculate or POST another version.
retry_tag="$(run_release_tag)"
[ "$retry_tag" = "$first_tag" ]
[ "$(cat "$TEST_DIR/autotag-count")" = "1" ]
[ "$(cat "$TEST_DIR/post-count")" = "1" ]

if EXPECTED_RELEASE_TAG=2.0.0 run_release_tag >"$TEST_DIR/existing.out" 2>"$TEST_DIR/existing.err"; then
  echo "release tag reused a different numeric tag than the explicit request" >&2
  exit 1
fi
grep -F 'existing release tag 1.2.3 does not match expected release tag 2.0.0' \
  "$TEST_DIR/existing.err" >/dev/null
[ "$(cat "$TEST_DIR/autotag-count")" = "1" ]
[ "$(cat "$TEST_DIR/post-count")" = "1" ]

later_main_sha="1111111111111111111111111111111111111111"
if FAKE_HEAD_SHA="$later_main_sha" run_release_tag >"$TEST_DIR/later.out" 2>"$TEST_DIR/later.err"; then
  echo "release tag accepted a later main HEAD instead of the reviewed release source" >&2
  exit 1
fi
grep -F 'reviewed release SHA is not the checked-out release commit' "$TEST_DIR/later.err" >/dev/null
[ "$(cat "$TEST_DIR/autotag-count")" = "1" ]
[ "$(cat "$TEST_DIR/post-count")" = "1" ]

printf '%s\n' 'fedcba9876543210fedcba9876543210fedcba98' >"$TEST_DIR/remote-sha"
if run_release_tag >"$TEST_DIR/mismatch.out" 2>"$TEST_DIR/mismatch.err"; then
  echo "release tag retry accepted a remote tag moved to another commit" >&2
  exit 1
fi
grep -F 'remote release tag does not point at the reviewed main commit' "$TEST_DIR/mismatch.err" >/dev/null

# An explicit release request must fail before its first remote mutation when
# AutoTag computes a different numeric version.
: >"$TEST_DIR/local-tag"
if EXPECTED_RELEASE_TAG=2.0.0 run_release_tag >"$TEST_DIR/version.out" 2>"$TEST_DIR/version.err"; then
  echo "release tag accepted an unexpected AutoTag version" >&2
  exit 1
fi
grep -F 'AutoTag created 1.2.3 instead of expected release tag 2.0.0' "$TEST_DIR/version.err" >/dev/null
[ "$(cat "$TEST_DIR/autotag-count")" = "2" ]
[ "$(cat "$TEST_DIR/post-count")" = "1" ]

echo "Release tag creation is numeric, exact-version, retry-idempotent, and refuses moved tags."
