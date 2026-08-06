#!/usr/bin/env bash

set -euo pipefail

readonly SOURCE_PATH="web/e2e/deployed-readiness.mjs"
readonly MAX_SOURCE_BYTES=262144
readonly MAX_RESPONSE_BYTES=524288

fail() {
  printf 'prepare browser readiness source failed: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

[[ "$#" -eq 1 ]] || fail "expected one immutable source commit"
source_sha="$1"
protected_sha="${PROTECTED_SOURCE_SHA:-}"
repository="${GITHUB_REPOSITORY:-}"
contents_token="${BROWSER_READINESS_CONTENTS_TOKEN:-}"
runner_temp="${RUNNER_TEMP:-}"
github_env="${GITHUB_ENV:-}"

[[ "$source_sha" =~ ^[0-9a-f]{40}$ ]] || fail "source commit must be an immutable SHA"
[[ "$protected_sha" =~ ^[0-9a-f]{40}$ ]] || fail "protected commit must be an immutable SHA"
[[ "$repository" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || fail "repository identity is invalid"
[[ -n "$contents_token" ]] || fail "contents token is required"
[[ "$runner_temp" == /* && -d "$runner_temp" && ! -L "$runner_temp" ]] || fail "runner temp directory is invalid"
[[ "$github_env" == /* && -f "$github_env" && ! -L "$github_env" ]] || fail "GitHub environment file is invalid"

for command in awk base64 curl git install jq mktemp tr wc; do
  require_command "$command"
done

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"
[[ "$(git rev-parse --verify 'HEAD^{commit}')" == "$protected_sha" ]] || fail "checkout is not the protected commit"
[[ "$(git ls-files --stage -- "$SOURCE_PATH" | awk '{ print $1 ":" $4 }')" == "100644:${SOURCE_PATH}" ]] ||
  fail "protected source path is not one regular tracked file"
[[ -f "$SOURCE_PATH" && ! -L "$SOURCE_PATH" ]] || fail "protected source path is not a regular file"
[[ -z "$(git status --short --untracked-files=no)" ]] || fail "protected checkout has tracked changes"

umask 077
commit_response_path=""
root_tree_response_path=""
web_tree_response_path=""
e2e_tree_response_path=""
contents_response_path=""
candidate_path=""
cleanup() {
  local path
  for path in \
    "$commit_response_path" \
    "$root_tree_response_path" \
    "$web_tree_response_path" \
    "$e2e_tree_response_path" \
    "$contents_response_path" \
    "$candidate_path"; do
    [[ -z "$path" ]] || rm -f -- "$path"
  done
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
commit_response_path="$(mktemp "${runner_temp}/scribe-browser-readiness-commit.XXXXXX")"
root_tree_response_path="$(mktemp "${runner_temp}/scribe-browser-readiness-root-tree.XXXXXX")"
web_tree_response_path="$(mktemp "${runner_temp}/scribe-browser-readiness-web-tree.XXXXXX")"
e2e_tree_response_path="$(mktemp "${runner_temp}/scribe-browser-readiness-e2e-tree.XXXXXX")"
contents_response_path="$(mktemp "${runner_temp}/scribe-browser-readiness-contents.XXXXXX")"
candidate_path="$(mktemp "${runner_temp}/scribe-browser-readiness-source.XXXXXX")"
for private_path in \
  "$commit_response_path" \
  "$root_tree_response_path" \
  "$web_tree_response_path" \
  "$e2e_tree_response_path" \
  "$contents_response_path" \
  "$candidate_path"; do
  [[ -f "$private_path" && ! -L "$private_path" ]] || fail "private source files are invalid"
done

github_api_request() {
  local url="$1"
  local output_path="$2"
  local response_size

  curl --disable \
    --fail \
    --silent \
    --show-error \
    --connect-timeout 10 \
    --max-time 20 \
    --retry 2 \
    --retry-all-errors \
    --retry-delay 1 \
    --retry-max-time 45 \
    --max-filesize "$MAX_RESPONSE_BYTES" \
    --proto '=https' \
    --tlsv1.2 \
    --header "Accept: application/vnd.github+json" \
    --header "Authorization: Bearer ${contents_token}" \
    --header "X-GitHub-Api-Version: 2022-11-28" \
    --output "$output_path" \
    "$url" || fail "GitHub API request failed"

  [[ -f "$output_path" && ! -L "$output_path" ]] || fail "GitHub API response is not a private regular file"
  response_size="$(wc -c <"$output_path" | tr -d '[:space:]')"
  [[ "$response_size" =~ ^[0-9]+$ && "$response_size" -ge 1 && "$response_size" -le "$MAX_RESPONSE_BYTES" ]] ||
    fail "GitHub API response size is invalid"
}

require_tree_entry() {
  local response_path="$1"
  local expected_tree_sha="$2"
  local entry_path="$3"
  local entry_mode="$4"
  local entry_type="$5"

  jq -er \
    --arg tree_sha "$expected_tree_sha" \
    --arg path "$entry_path" \
    --arg mode "$entry_mode" \
    --arg entry_type "$entry_type" '
      select(
        type == "object"
        and .sha == $tree_sha
        and .truncated == false
        and (.tree | type == "array")
      )
      | [.tree[] | select(.path == $path)]
      | select(length == 1)
      | .[0]
      | select(
          type == "object"
          and .mode == $mode
          and .type == $entry_type
          and (.sha | type == "string" and test("^[0-9a-f]{40}$"))
        )
      | .sha
    ' "$response_path" || fail "GitHub tree does not contain the required regular source path"
}

commit_url="https://api.github.com/repos/${repository}/git/commits/${source_sha}"
github_api_request "$commit_url" "$commit_response_path"
root_tree_sha="$(
  jq -er \
    --arg source_sha "$source_sha" '
      select(
        type == "object"
        and .sha == $source_sha
        and (.tree | type == "object")
        and (.tree.sha | type == "string" and test("^[0-9a-f]{40}$"))
      )
      | .tree.sha
    ' "$commit_response_path"
)" || fail "GitHub did not return the exact source commit"

root_tree_url="https://api.github.com/repos/${repository}/git/trees/${root_tree_sha}"
github_api_request "$root_tree_url" "$root_tree_response_path"
web_tree_sha="$(require_tree_entry "$root_tree_response_path" "$root_tree_sha" "web" "040000" "tree")"

web_tree_url="https://api.github.com/repos/${repository}/git/trees/${web_tree_sha}"
github_api_request "$web_tree_url" "$web_tree_response_path"
e2e_tree_sha="$(require_tree_entry "$web_tree_response_path" "$web_tree_sha" "e2e" "040000" "tree")"

e2e_tree_url="https://api.github.com/repos/${repository}/git/trees/${e2e_tree_sha}"
github_api_request "$e2e_tree_url" "$e2e_tree_response_path"
source_blob_sha="$(require_tree_entry "$e2e_tree_response_path" "$e2e_tree_sha" "deployed-readiness.mjs" "100644" "blob")"
source_tree_size="$(
  jq -er \
    --arg path "deployed-readiness.mjs" \
    --arg blob_sha "$source_blob_sha" \
    --argjson maximum "$MAX_SOURCE_BYTES" '
      [.tree[] | select(.path == $path and .sha == $blob_sha)]
      | select(length == 1)
      | .[0].size
      | select(type == "number" and . >= 1 and . <= $maximum and . == floor)
      | tostring
    ' "$e2e_tree_response_path"
)" || fail "GitHub tree source size is invalid"

contents_url="https://api.github.com/repos/${repository}/contents/${SOURCE_PATH}?ref=${source_sha}"
github_api_request "$contents_url" "$contents_response_path"

jq -e \
  --arg path "$SOURCE_PATH" \
  --arg blob_sha "$source_blob_sha" \
  --argjson source_size "$source_tree_size" '
    type == "object"
    and .type == "file"
    and .encoding == "base64"
    and .path == $path
    and .name == "deployed-readiness.mjs"
    and .sha == $blob_sha
    and (.content | type == "string")
    and .size == $source_size
  ' "$contents_response_path" >/dev/null ||
  fail "GitHub Contents API response does not match the exact source tree blob"

jq -jr '.content' "$contents_response_path" |
  base64 --decode >"$candidate_path" || fail "source content is not valid base64"
[[ "$(wc -c <"$candidate_path" | tr -d '[:space:]')" == "$source_tree_size" ]] ||
  fail "source size does not match the exact source tree blob"
[[ "$(git hash-object --no-filters "$candidate_path")" == "$source_blob_sha" ]] ||
  fail "source content does not match the exact source tree blob"

install -m 0644 "$candidate_path" "$SOURCE_PATH"
[[ -f "$SOURCE_PATH" && ! -L "$SOURCE_PATH" ]] || fail "staged source is not a regular file"
[[ "$(git hash-object --no-filters "$SOURCE_PATH")" == "$source_blob_sha" ]] ||
  fail "staged source blob changed during installation"
changed_paths="$(git diff --name-only --)"
[[ -z "$changed_paths" || "$changed_paths" == "$SOURCE_PATH" ]] || fail "staging changed a protected path"

{
  printf 'SCRIBE_BROWSER_READINESS_SOURCE_SHA=%s\n' "$source_sha"
  printf 'SCRIBE_BROWSER_READINESS_SCRIPT_BLOB_SHA=%s\n' "$source_blob_sha"
} >>"$github_env"
