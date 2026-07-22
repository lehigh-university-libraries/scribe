#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_ROOT="$(mktemp -d)"
trap 'rm -rf "$TEST_ROOT"' EXIT
mkdir -p "$TEST_ROOT/bin" "$TEST_ROOT/state"

cat >"$TEST_ROOT/bin/curl" <<'FAKE_CURL'
#!/usr/bin/env bash
set -euo pipefail

output=""
method="GET"
payload=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) output="$2"; shift 2 ;;
    -w|-H|--connect-timeout|--max-time) shift 2 ;;
    -X) method="$2"; shift 2 ;;
    --data) payload="$2"; shift 2 ;;
    -sS) shift ;;
    http://*|https://*) url="$1"; shift ;;
    *) echo "unexpected fake curl argument: $1" >&2; exit 2 ;;
  esac
done
[ -n "$output" ] && [ -n "$url" ]
endpoint="/${url#*://*/}"
case "$endpoint" in
  /v1/secret/data/*)
    key="${endpoint#/v1/secret/data/}"
    file="${FAKE_VAULT_ROOT}/data/${key}.json"
    case "$method" in
      GET)
        if [ -f "$file" ]; then
          jq -cn --slurpfile value "$file" '{data: {data: $value[0]}}' >"$output"
          printf 200
        else
          printf '{}' >"$output"
          printf 404
        fi
        ;;
      PUT)
        mkdir -p "$(dirname "$file")"
        jq '.data' <<<"$payload" >"$file"
        printf '{}' >"$output"
        printf 200
        ;;
      *) exit 2 ;;
    esac
    ;;
  /v1/secret/metadata/*)
    key="${endpoint#/v1/secret/metadata/}"
    directory="${FAKE_VAULT_ROOT}/data/${key}"
    case "$method" in
      LIST)
        if [ ! -d "$directory" ]; then
          printf '{}' >"$output"
          printf 404
          exit 0
        fi
        keys="$({
          find "$directory" -mindepth 1 -maxdepth 1 -printf '%f\n' \
            | sed -E 's/\.json$//'
          find "$directory" -mindepth 1 -maxdepth 1 -type d -printf '%f/\n'
        } | sort -u | jq -Rsc 'split("\n") | map(select(length > 0))')"
        jq -cn --argjson keys "$keys" '{data: {keys: $keys}}' >"$output"
        printf 200
        ;;
      DELETE)
        rm -f "${directory}.json"
        printf '{}' >"$output"
        printf 204
        ;;
      *) exit 2 ;;
    esac
    ;;
  *) echo "unexpected fake Vault endpoint: $endpoint" >&2; exit 2 ;;
esac
FAKE_CURL
chmod +x "$TEST_ROOT/bin/curl"

run_preview_secrets() {
  PATH="$TEST_ROOT/bin:$PATH" \
    FAKE_VAULT_ROOT="$TEST_ROOT/state" \
    VAULT_ADDR="https://vault.example" \
    VAULT_TOKEN="test-token" \
    VAULT_ADMIN_TOKEN="test-admin-header" \
    bash "$ROOT_DIR/ci/preview-vault-secrets.sh" "$@"
}

preview_prefix="scribe/previews/scribe-pr-75@example-project.iam.gserviceaccount.com"
run_preview_secrets ensure "$preview_prefix" >/dev/null
database_file="$TEST_ROOT/state/data/${preview_prefix}/database/app.json"
oauth_file="$TEST_ROOT/state/data/${preview_prefix}/google_oauth.json"
[ -s "$database_file" ] || { echo "Preview bootstrap did not populate an empty namespace" >&2; exit 1; }
[ ! -e "$oauth_file" ] || { echo "Preview bootstrap created a reusable OAuth credential" >&2; exit 1; }
jq -e '.password | length == 64' "$database_file" >/dev/null

first_database_digest="$(sha256sum "$database_file")"
run_preview_secrets ensure "$preview_prefix" >/dev/null
[ "$(sha256sum "$database_file")" = "$first_database_digest" ] || { echo "Preview reapply rotated the database credential" >&2; exit 1; }

mkdir -p "$TEST_ROOT/state/data/${preview_prefix}/provider-secrets/workspaces/8"
printf '{"token":"fixture"}\n' >"$TEST_ROOT/state/data/${preview_prefix}/provider-secrets/workspaces/8/openai.json"
run_preview_secrets delete "$preview_prefix" >/dev/null
if find "$TEST_ROOT/state/data/${preview_prefix}" -type f -print -quit 2>/dev/null | grep -q .; then
  echo "Preview teardown left Vault data behind" >&2
  exit 1
fi

if run_preview_secrets ensure scribe/previews/scribe-prod@example-project.iam.gserviceaccount.com >/dev/null 2>&1; then
  echo "Preview bootstrap accepted a production prefix" >&2
  exit 1
fi

if run_preview_secrets ensure scribe/previews/scribe-pr-75@other.example.com >/dev/null 2>&1; then
  echo "Preview bootstrap accepted a non-GCP service-account namespace" >&2
  exit 1
fi

echo "Preview Vault bootstrap and teardown contracts passed."
