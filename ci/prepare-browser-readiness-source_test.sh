#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d)"
readonly ROOT_DIR TEST_DIR
readonly SOURCE_PATH="web/e2e/deployed-readiness.mjs"
readonly SOURCE_SHA="1111111111111111111111111111111111111111"
readonly ROOT_TREE_SHA="2222222222222222222222222222222222222222"
readonly WEB_TREE_SHA="3333333333333333333333333333333333333333"
readonly E2E_TREE_SHA="4444444444444444444444444444444444444444"
readonly REPOSITORY="libops/scribe"
readonly CONTENTS_TOKEN="test-only-contents-token"

cleanup() {
  rm -rf -- "$TEST_DIR"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

fail() {
  printf 'prepare browser readiness source test failed: %s\n' "$*" >&2
  exit 1
}

mock_bin="$TEST_DIR/mock-bin"
mkdir -p "$mock_bin"

cat >"$mock_bin/curl" <<'MOCK_CURL'
#!/usr/bin/env bash

set -euo pipefail

output=""
url=""
authorization=""
disabled_config=false

while (($# > 0)); do
  case "$1" in
    --disable)
      disabled_config=true
      shift
      ;;
    --fail|--silent|--show-error|--retry-all-errors|--tlsv1.2)
      shift
      ;;
    --connect-timeout|--max-time|--retry|--retry-delay|--retry-max-time|--max-filesize|--proto)
      (($# >= 2)) || exit 90
      shift 2
      ;;
    --header)
      (($# >= 2)) || exit 91
      if [[ "$2" == Authorization:* ]]; then
        authorization="$2"
      fi
      shift 2
      ;;
    --output)
      (($# >= 2)) || exit 92
      output="$2"
      shift 2
      ;;
    https://*)
      [[ -z "$url" ]] || exit 93
      url="$1"
      shift
      ;;
    *)
      printf 'mock curl rejected unexpected argument\n' >&2
      exit 94
      ;;
  esac
done

[[ "$disabled_config" == true ]] || exit 95
[[ "$authorization" == "Authorization: Bearer ${FAKE_EXPECTED_TOKEN}" ]] || exit 97
[[ "$output" == "$FAKE_EXPECTED_OUTPUT_PREFIX"* ]] || exit 98
case "$url" in
  "$FAKE_EXPECTED_COMMIT_URL") response="$FAKE_COMMIT_RESPONSE" ;;
  "$FAKE_EXPECTED_ROOT_TREE_URL") response="$FAKE_ROOT_TREE_RESPONSE" ;;
  "$FAKE_EXPECTED_WEB_TREE_URL") response="$FAKE_WEB_TREE_RESPONSE" ;;
  "$FAKE_EXPECTED_E2E_TREE_URL") response="$FAKE_E2E_TREE_RESPONSE" ;;
  "$FAKE_EXPECTED_CONTENTS_URL") response="$FAKE_CONTENTS_RESPONSE" ;;
  *) exit 96 ;;
esac
[[ -f "$response" && ! -L "$response" ]] || exit 99
install -m 0600 "$response" "$output"
printf '%s\n' "$url" >>"$FAKE_CURL_LOG"
MOCK_CURL
chmod 0700 "$mock_bin/curl"

new_fixture() {
  local name="$1"
  fixture="$TEST_DIR/$name/repository"
  runner_temp="$TEST_DIR/$name/runner-temp"
  github_env="$TEST_DIR/$name/github-env"
  commit_response="$TEST_DIR/$name/commit-response.json"
  root_tree_response="$TEST_DIR/$name/root-tree-response.json"
  web_tree_response="$TEST_DIR/$name/web-tree-response.json"
  e2e_tree_response="$TEST_DIR/$name/e2e-tree-response.json"
  contents_response="$TEST_DIR/$name/contents-response.json"
  curl_log="$TEST_DIR/$name/curl.log"
  execution_marker="$TEST_DIR/$name/untrusted-script-executed"

  mkdir -p "$fixture/ci" "$fixture/web/e2e" "$runner_temp"
  install -m 0644 "$ROOT_DIR/ci/prepare-browser-readiness-source.sh" \
    "$fixture/ci/prepare-browser-readiness-source.sh"
  printf '%s\n' 'console.log("protected source");' >"$fixture/$SOURCE_PATH"
  printf '%s\n' 'protected fixture' >"$fixture/protected.txt"
  : >"$github_env"
  : >"$curl_log"

  git -C "$fixture" init -q
  git -C "$fixture" config user.email test@example.invalid
  git -C "$fixture" config user.name 'Browser Readiness Contract'
  git -C "$fixture" add ci/prepare-browser-readiness-source.sh "$SOURCE_PATH" protected.txt
  git -C "$fixture" commit -qm 'protected fixture'
  protected_sha="$(git -C "$fixture" rev-parse HEAD)"
  original_blob_sha="$(git -C "$fixture" hash-object --no-filters "$fixture/$SOURCE_PATH")"
}

create_candidate() {
  local candidate_path="$1"
  cat >"$candidate_path" <<'UNTRUSTED_SOURCE'
import { writeFileSync } from "node:fs";

writeFileSync(process.env.UNTRUSTED_EXECUTION_MARKER, "executed");
UNTRUSTED_SOURCE
}

create_response() {
  local candidate_path="$1"
  local output_path="$2"
  local encoded size blob_sha
  encoded="$(base64 <"$candidate_path")"
  size="$(wc -c <"$candidate_path" | tr -d '[:space:]')"
  blob_sha="$(git hash-object --no-filters "$candidate_path")"
  jq -n \
    --arg content "$encoded" \
    --arg name 'deployed-readiness.mjs' \
    --arg path "$SOURCE_PATH" \
    --arg sha "$blob_sha" \
    --argjson size "$size" \
    '{type:"file", encoding:"base64", path:$path, name:$name, sha:$sha, size:$size, content:$content}' \
    >"$output_path"
}

create_tree_responses() {
  local candidate_path="$1"
  local size blob_sha
  size="$(wc -c <"$candidate_path" | tr -d '[:space:]')"
  blob_sha="$(git hash-object --no-filters "$candidate_path")"

  jq -n \
    --arg commit_sha "$SOURCE_SHA" \
    --arg tree_sha "$ROOT_TREE_SHA" \
    '{sha:$commit_sha, tree:{sha:$tree_sha}}' \
    >"$commit_response"
  jq -n \
    --arg tree_sha "$ROOT_TREE_SHA" \
    --arg child_sha "$WEB_TREE_SHA" \
    '{sha:$tree_sha, truncated:false, tree:[{path:"web", mode:"040000", type:"tree", sha:$child_sha}]}' \
    >"$root_tree_response"
  jq -n \
    --arg tree_sha "$WEB_TREE_SHA" \
    --arg child_sha "$E2E_TREE_SHA" \
    '{sha:$tree_sha, truncated:false, tree:[{path:"e2e", mode:"040000", type:"tree", sha:$child_sha}]}' \
    >"$web_tree_response"
  jq -n \
    --arg tree_sha "$E2E_TREE_SHA" \
    --arg blob_sha "$blob_sha" \
    --argjson size "$size" \
    '{
      sha:$tree_sha,
      truncated:false,
      tree:[{
        path:"deployed-readiness.mjs",
        mode:"100644",
        type:"blob",
        sha:$blob_sha,
        size:$size
      }]
    }' \
    >"$e2e_tree_response"
}

create_fixture_responses() {
  local candidate_path="$1"
  create_tree_responses "$candidate_path"
  create_response "$candidate_path" "$contents_response"
}

run_helper() {
  local requested_sha="${1:-$SOURCE_SHA}"
  local expected_contents_url
  expected_contents_url="https://api.github.com/repos/${REPOSITORY}/contents/"
  expected_contents_url+="${SOURCE_PATH}?ref=${requested_sha}"
  env \
    PATH="$mock_bin:$PATH" \
    BROWSER_READINESS_CONTENTS_TOKEN="$CONTENTS_TOKEN" \
    FAKE_COMMIT_RESPONSE="$commit_response" \
    FAKE_ROOT_TREE_RESPONSE="$root_tree_response" \
    FAKE_WEB_TREE_RESPONSE="$web_tree_response" \
    FAKE_E2E_TREE_RESPONSE="$e2e_tree_response" \
    FAKE_CONTENTS_RESPONSE="$contents_response" \
    FAKE_CURL_LOG="$curl_log" \
    FAKE_EXPECTED_OUTPUT_PREFIX="$runner_temp/scribe-browser-readiness-" \
    FAKE_EXPECTED_TOKEN="$CONTENTS_TOKEN" \
    FAKE_EXPECTED_COMMIT_URL="https://api.github.com/repos/${REPOSITORY}/git/commits/${requested_sha}" \
    FAKE_EXPECTED_ROOT_TREE_URL="https://api.github.com/repos/${REPOSITORY}/git/trees/${ROOT_TREE_SHA}" \
    FAKE_EXPECTED_WEB_TREE_URL="https://api.github.com/repos/${REPOSITORY}/git/trees/${WEB_TREE_SHA}" \
    FAKE_EXPECTED_E2E_TREE_URL="https://api.github.com/repos/${REPOSITORY}/git/trees/${E2E_TREE_SHA}" \
    FAKE_EXPECTED_CONTENTS_URL="$expected_contents_url" \
    GITHUB_ENV="$github_env" \
    GITHUB_REPOSITORY="$REPOSITORY" \
    PROTECTED_SOURCE_SHA="$protected_sha" \
    RUNNER_TEMP="$runner_temp" \
    UNTRUSTED_EXECUTION_MARKER="$execution_marker" \
    bash "$fixture/ci/prepare-browser-readiness-source.sh" "$requested_sha"
}

assert_rejected_without_mutation() {
  local label="$1"
  local stderr_file="$TEST_DIR/${label}/stderr"
  local stdout_file="$TEST_DIR/${label}/stdout"

  if run_helper >"$stdout_file" 2>"$stderr_file"; then
    fail "$label was accepted"
  fi
  [[ "$(git -C "$fixture" hash-object --no-filters "$fixture/$SOURCE_PATH")" == "$original_blob_sha" ]] ||
    fail "$label changed the protected source"
  [[ ! -s "$github_env" ]] || fail "$label exported an unvalidated identity"
  [[ ! -e "$execution_marker" ]] || fail "$label executed untrusted source"
  [[ -z "$(find "$runner_temp" -mindepth 1 -maxdepth 1 -print -quit)" ]] ||
    fail "$label retained a private temporary file"
  if rg -Fq "$CONTENTS_TOKEN" "$stdout_file" "$stderr_file"; then
    fail "$label exposed the Contents API token"
  fi
}

new_fixture success
success_candidate="$TEST_DIR/success/pr-head-source.mjs"
mkdir -p "$(dirname "$success_candidate")"
create_candidate "$success_candidate"
create_fixture_responses "$success_candidate"
expected_blob_sha="$(git hash-object --no-filters "$success_candidate")"
run_helper
cmp -s "$success_candidate" "$fixture/$SOURCE_PATH" || fail "valid source was not staged exactly"
[[ -f "$fixture/$SOURCE_PATH" && ! -L "$fixture/$SOURCE_PATH" ]] || fail "staged source is not a regular file"
[[ "$(stat -c '%a' "$fixture/$SOURCE_PATH")" == 644 ]] || fail "staged source does not have fixed permissions"
[[ ! -e "$execution_marker" ]] || fail "the credentialed preparation phase executed untrusted source"
[[ "$(git -C "$fixture" diff --name-only --)" == "$SOURCE_PATH" ]] || fail "valid staging changed another path"
[[ "$(cat "$github_env")" == "$(
  printf 'SCRIBE_BROWSER_READINESS_SOURCE_SHA=%s\n' "$SOURCE_SHA"
  printf 'SCRIBE_BROWSER_READINESS_SCRIPT_BLOB_SHA=%s' "$expected_blob_sha"
)" ]] || fail "valid staging exported unexpected environment data"
[[ "$(cat "$curl_log")" == "$({
  printf 'https://api.github.com/repos/%s/git/commits/%s\n' "$REPOSITORY" "$SOURCE_SHA"
  printf 'https://api.github.com/repos/%s/git/trees/%s\n' "$REPOSITORY" "$ROOT_TREE_SHA"
  printf 'https://api.github.com/repos/%s/git/trees/%s\n' "$REPOSITORY" "$WEB_TREE_SHA"
  printf 'https://api.github.com/repos/%s/git/trees/%s\n' "$REPOSITORY" "$E2E_TREE_SHA"
  printf 'https://api.github.com/repos/%s/contents/%s?ref=%s\n' "$REPOSITORY" "$SOURCE_PATH" "$SOURCE_SHA"
})" ]] || fail "preparation did not walk the exact source commit tree before requesting Contents"
[[ -z "$(find "$runner_temp" -mindepth 1 -maxdepth 1 -print -quit)" ]] ||
  fail "valid staging retained a private temporary file"

for rejection in \
  wrong-path \
  wrong-name \
  wrong-type \
  wrong-encoding \
  oversize \
  declared-size \
  blob-sha \
  malformed-base64 \
  parent-symlink \
  parent-gitlink \
  source-symlink \
  source-gitlink \
  duplicate-source-entry \
  truncated-tree \
  tree-oversize \
  commit-mismatch; do
  new_fixture "$rejection"
  rejection_candidate="$TEST_DIR/$rejection/pr-head-source.mjs"
  mkdir -p "$(dirname "$rejection_candidate")"
  create_candidate "$rejection_candidate"
  create_fixture_responses "$rejection_candidate"
  case "$rejection" in
    wrong-path) jq '.path = "web/e2e/other.mjs"' "$contents_response" >"$contents_response.next" ;;
    wrong-name) jq '.name = "other.mjs"' "$contents_response" >"$contents_response.next" ;;
    wrong-type) jq '.type = "dir"' "$contents_response" >"$contents_response.next" ;;
    wrong-encoding) jq '.encoding = "utf-8"' "$contents_response" >"$contents_response.next" ;;
    oversize) jq '.size = 262145' "$contents_response" >"$contents_response.next" ;;
    declared-size) jq '.size += 1' "$contents_response" >"$contents_response.next" ;;
    blob-sha) jq '.sha = "0000000000000000000000000000000000000000"' "$contents_response" >"$contents_response.next" ;;
    malformed-base64) jq '.content = "%%%%"' "$contents_response" >"$contents_response.next" ;;
    parent-symlink)
      jq '.tree[0].mode = "120000" | .tree[0].type = "blob"' \
        "$root_tree_response" >"$root_tree_response.next"
      ;;
    parent-gitlink)
      jq '.tree[0].mode = "160000" | .tree[0].type = "commit"' \
        "$web_tree_response" >"$web_tree_response.next"
      ;;
    source-symlink) jq '.tree[0].mode = "120000"' "$e2e_tree_response" >"$e2e_tree_response.next" ;;
    source-gitlink)
      jq '.tree[0].mode = "160000" | .tree[0].type = "commit"' \
        "$e2e_tree_response" >"$e2e_tree_response.next"
      ;;
    duplicate-source-entry) jq '.tree += [.tree[0]]' "$e2e_tree_response" >"$e2e_tree_response.next" ;;
    truncated-tree) jq '.truncated = true' "$e2e_tree_response" >"$e2e_tree_response.next" ;;
    tree-oversize) jq '.tree[0].size = 262145' "$e2e_tree_response" >"$e2e_tree_response.next" ;;
    commit-mismatch)
      jq '.sha = "0000000000000000000000000000000000000000"' \
        "$commit_response" >"$commit_response.next"
      ;;
  esac
  for response_path in \
    "$commit_response" \
    "$contents_response" \
    "$root_tree_response" \
    "$web_tree_response" \
    "$e2e_tree_response"; do
    [[ ! -e "$response_path.next" ]] || mv "$response_path.next" "$response_path"
  done
  assert_rejected_without_mutation "$rejection"
done

new_fixture invalid-source-sha
invalid_candidate="$TEST_DIR/invalid-source-sha/pr-head-source.mjs"
create_candidate "$invalid_candidate"
create_fixture_responses "$invalid_candidate"
if run_helper main >"$TEST_DIR/invalid-source-sha/stdout" 2>"$TEST_DIR/invalid-source-sha/stderr"; then
  fail "mutable source ref was accepted"
fi
[[ ! -s "$curl_log" ]] || fail "mutable source ref reached the Contents API"
[[ "$(git -C "$fixture" hash-object --no-filters "$fixture/$SOURCE_PATH")" == "$original_blob_sha" ]] ||
  fail "mutable source ref changed the protected source"
[[ ! -s "$github_env" ]] || fail "mutable source ref exported an identity"

new_fixture dirty-checkout
dirty_candidate="$TEST_DIR/dirty-checkout/pr-head-source.mjs"
create_candidate "$dirty_candidate"
create_fixture_responses "$dirty_candidate"
printf '%s\n' 'credentialed checkout was modified' >"$fixture/protected.txt"
assert_rejected_without_mutation dirty-checkout
[[ ! -s "$curl_log" ]] || fail "dirty protected checkout reached the Contents API"

new_fixture oversized-response
oversized_candidate="$TEST_DIR/oversized-response/pr-head-source.mjs"
create_candidate "$oversized_candidate"
create_fixture_responses "$oversized_candidate"
truncate -s 524289 "$contents_response"
assert_rejected_without_mutation oversized-response

printf 'Exact PR-head browser readiness source preparation passed.\n'
