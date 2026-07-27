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
request_path="${endpoint%%\?*}"
query=""
if [[ "$endpoint" == *\?* ]]; then
  query="${endpoint#*\?}"
fi
printf '%s %s\n' "$method" "$endpoint" >>"${FAKE_VAULT_REQUEST_LOG}"

if [ -n "${FAKE_VAULT_FAILURE_METHOD:-}" ] \
  && [ "$method" = "$FAKE_VAULT_FAILURE_METHOD" ] \
  && { [ -z "${FAKE_VAULT_FAILURE_ENDPOINT:-}" ] \
    || [ "$endpoint" = "$FAKE_VAULT_FAILURE_ENDPOINT" ]; }; then
  attempt=0
  if [ -f "${FAKE_VAULT_FAILURE_COUNT_FILE}" ]; then
    read -r attempt <"${FAKE_VAULT_FAILURE_COUNT_FILE}" || true
  fi
  attempt=$((attempt + 1))
  printf '%s\n' "$attempt" >"${FAKE_VAULT_FAILURE_COUNT_FILE}"
  if [ "$attempt" -le "${FAKE_VAULT_FAILURES:-0}" ]; then
    if [ "${FAKE_VAULT_FAILURE_STATUS:-502}" = "transport" ]; then
      echo 'DO-NOT-LOG-CURL-DIAGNOSTIC' >&2
      exit 7
    fi
    printf '{"errors":["DO-NOT-LOG-VAULT-RESPONSE"]}' >"$output"
    printf '%s' "${FAKE_VAULT_FAILURE_STATUS:-502}"
    exit 0
  fi
fi

case "$request_path" in
  /v1/secret/data/*)
    key="${request_path#/v1/secret/data/}"
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
    key="${request_path#/v1/secret/metadata/}"
    directory="${FAKE_VAULT_ROOT}/data/${key}"
    case "$method" in
      GET)
        [ "$query" = "list=true" ] || exit 2
        if [ ! -d "$directory" ]; then
          printf '{}' >"$output"
          printf 404
          exit 0
        fi
        keys="$({
          find "$directory" -mindepth 1 -maxdepth 1 -type f -name '*.json' -printf '%f\n' \
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

cat >"$TEST_ROOT/bin/sleep" <<'FAKE_SLEEP'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$1" >>"${FAKE_VAULT_SLEEP_LOG}"
FAKE_SLEEP
chmod +x "$TEST_ROOT/bin/sleep"

request_log="$TEST_ROOT/requests.log"
sleep_log="$TEST_ROOT/sleeps.log"
failure_count_file="$TEST_ROOT/failure-count"
: >"$request_log"
: >"$sleep_log"

run_preview_secrets() {
  PATH="$TEST_ROOT/bin:$PATH" \
    FAKE_VAULT_ROOT="$TEST_ROOT/state" \
    FAKE_VAULT_REQUEST_LOG="$request_log" \
    FAKE_VAULT_SLEEP_LOG="$sleep_log" \
    FAKE_VAULT_FAILURE_COUNT_FILE="$failure_count_file" \
    FAKE_VAULT_FAILURE_METHOD="${FAKE_VAULT_FAILURE_METHOD:-}" \
    FAKE_VAULT_FAILURE_ENDPOINT="${FAKE_VAULT_FAILURE_ENDPOINT:-}" \
    FAKE_VAULT_FAILURES="${FAKE_VAULT_FAILURES:-0}" \
    FAKE_VAULT_FAILURE_STATUS="${FAKE_VAULT_FAILURE_STATUS:-502}" \
    VAULT_HTTP_RETRY_ATTEMPTS="${VAULT_HTTP_RETRY_ATTEMPTS:-4}" \
    VAULT_HTTP_RETRY_INITIAL_DELAY_SECONDS="${VAULT_HTTP_RETRY_INITIAL_DELAY_SECONDS:-1}" \
    VAULT_HTTP_RETRY_MAX_DELAY_SECONDS="${VAULT_HTTP_RETRY_MAX_DELAY_SECONDS:-4}" \
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
if grep -Eq '^LIST ' "$request_log"; then
  echo "Preview teardown used the Cloud Run-incompatible LIST method" >&2
  exit 1
fi

# Cloud Run rejects the non-standard LIST verb before Vault receives it. The
# equivalent GET ?list=true request must be used and must still retry a genuine
# transient 502 with a bound before finishing deletion.
root_metadata_endpoint="/v1/secret/metadata/${preview_prefix}"
root_metadata_list_endpoint="${root_metadata_endpoint}?list=true"
leaf_metadata_endpoint="${root_metadata_endpoint}/database/app"
mkdir -p "$TEST_ROOT/state/data/${preview_prefix}/database"
printf '{"password":"fixture"}\n' >"$database_file"
rm -f "$failure_count_file"
: >"$request_log"
: >"$sleep_log"
if ! FAKE_VAULT_FAILURE_METHOD=GET \
  FAKE_VAULT_FAILURE_ENDPOINT="$root_metadata_list_endpoint" FAKE_VAULT_FAILURES=2 \
  VAULT_HTTP_RETRY_ATTEMPTS=3 VAULT_HTTP_RETRY_INITIAL_DELAY_SECONDS=0 \
  VAULT_HTTP_RETRY_MAX_DELAY_SECONDS=0 run_preview_secrets delete "$preview_prefix" \
    >"$TEST_ROOT/list-retry.out" 2>"$TEST_ROOT/list-retry.err"; then
  echo "Preview Vault teardown did not recover from a transient root listing 502" >&2
  exit 1
fi
[ "$(grep -Fxc "GET ${root_metadata_list_endpoint}" "$request_log")" -eq 3 ]
[ "$(wc -l <"$sleep_log")" -eq 2 ]
grep -F 'Vault GET request received transient HTTP 502; retrying (1/3).' \
  "$TEST_ROOT/list-retry.err" >/dev/null
if grep -F 'DO-NOT-LOG-VAULT-RESPONSE' "$TEST_ROOT/list-retry.err" >/dev/null; then
  echo "Preview Vault listing retry leaked a response body" >&2
  exit 1
fi
[ ! -e "$database_file" ] || { echo "Retried root listing left Vault data behind" >&2; exit 1; }

# Metadata deletes are also idempotent and use the same bounded, redacted
# retry path.
mkdir -p "$TEST_ROOT/state/data/${preview_prefix}/database"
printf '{"password":"fixture"}\n' >"$database_file"
rm -f "$failure_count_file"
: >"$request_log"
: >"$sleep_log"
if ! FAKE_VAULT_FAILURE_METHOD=DELETE \
  FAKE_VAULT_FAILURE_ENDPOINT="$leaf_metadata_endpoint" FAKE_VAULT_FAILURES=2 \
  VAULT_HTTP_RETRY_ATTEMPTS=3 VAULT_HTTP_RETRY_INITIAL_DELAY_SECONDS=0 \
  VAULT_HTTP_RETRY_MAX_DELAY_SECONDS=0 run_preview_secrets delete "$preview_prefix" \
    >"$TEST_ROOT/delete-retry.out" 2>"$TEST_ROOT/delete-retry.err"; then
  echo "Preview Vault teardown did not recover from a transient DELETE 502" >&2
  exit 1
fi
delete_attempts="$(grep -Fxc "DELETE ${leaf_metadata_endpoint}" "$request_log")"
if [ "$delete_attempts" -ne 3 ]; then
  echo "Preview Vault teardown made ${delete_attempts} DELETE attempts instead of 3" >&2
  sed -n '1,40p' "$request_log" >&2
  sed -n '1,40p' "$TEST_ROOT/delete-retry.err" >&2
  exit 1
fi
[ "$(wc -l <"$sleep_log")" -eq 2 ]
grep -F 'Vault DELETE request received transient HTTP 502; retrying (1/3).' \
  "$TEST_ROOT/delete-retry.err" >/dev/null
if grep -F 'DO-NOT-LOG-VAULT-RESPONSE' "$TEST_ROOT/delete-retry.err" >/dev/null; then
  echo "Preview Vault retry leaked a response body" >&2
  exit 1
fi
[ ! -e "$database_file" ] || { echo "Retried preview cleanup left Vault data behind" >&2; exit 1; }

# Curl transport errors are also retried and their diagnostics remain
# redacted from protected logs.
mkdir -p "$TEST_ROOT/state/data/${preview_prefix}/database"
printf '{"password":"fixture"}\n' >"$database_file"
rm -f "$failure_count_file"
: >"$request_log"
: >"$sleep_log"
if ! FAKE_VAULT_FAILURE_METHOD=GET \
  FAKE_VAULT_FAILURE_ENDPOINT="$root_metadata_list_endpoint" \
  FAKE_VAULT_FAILURE_STATUS=transport FAKE_VAULT_FAILURES=1 \
  VAULT_HTTP_RETRY_ATTEMPTS=2 VAULT_HTTP_RETRY_INITIAL_DELAY_SECONDS=0 \
  VAULT_HTTP_RETRY_MAX_DELAY_SECONDS=0 run_preview_secrets delete "$preview_prefix" \
    >"$TEST_ROOT/transport-retry.out" 2>"$TEST_ROOT/transport-retry.err"; then
  echo "Preview Vault teardown did not recover from a curl transport error" >&2
  exit 1
fi
[ "$(grep -Fxc "GET ${root_metadata_list_endpoint}" "$request_log")" -eq 2 ]
grep -F 'Vault GET request encountered a transient transport error; retrying (1/2).' \
  "$TEST_ROOT/transport-retry.err" >/dev/null
if grep -F 'DO-NOT-LOG-CURL-DIAGNOSTIC' "$TEST_ROOT/transport-retry.err" >/dev/null; then
  echo "Preview Vault transport retry leaked curl diagnostics" >&2
  exit 1
fi
[ ! -e "$database_file" ] || { echo "Retried transport failure left Vault data behind" >&2; exit 1; }

# A non-transient authorization response fails immediately, while a persistent
# 5xx is bounded. Neither case may emit the Vault response body.
for status in 403 502; do
  mkdir -p "$TEST_ROOT/state/data/${preview_prefix}/database"
  printf '{"password":"fixture"}\n' >"$database_file"
  rm -f "$failure_count_file"
  : >"$request_log"
  : >"$sleep_log"
  failures=1
  expected_attempts=1
  if [ "$status" = 502 ]; then
    failures=9
    expected_attempts=3
  fi
  if FAKE_VAULT_FAILURE_METHOD=DELETE \
    FAKE_VAULT_FAILURE_ENDPOINT="$leaf_metadata_endpoint" FAKE_VAULT_FAILURES="$failures" \
    FAKE_VAULT_FAILURE_STATUS="$status" VAULT_HTTP_RETRY_ATTEMPTS=3 \
    VAULT_HTTP_RETRY_INITIAL_DELAY_SECONDS=0 VAULT_HTTP_RETRY_MAX_DELAY_SECONDS=0 \
    run_preview_secrets delete "$preview_prefix" \
      >"$TEST_ROOT/failure-${status}.out" 2>"$TEST_ROOT/failure-${status}.err"; then
    echo "Preview Vault teardown accepted HTTP ${status}" >&2
    exit 1
  fi
  [ "$(grep -Fxc "DELETE ${leaf_metadata_endpoint}" "$request_log")" -eq "$expected_attempts" ]
  if grep -F 'DO-NOT-LOG-VAULT-RESPONSE' "$TEST_ROOT/failure-${status}.err" >/dev/null; then
    echo "Preview Vault HTTP ${status} failure leaked a response body" >&2
    exit 1
  fi
done
grep -F 'Vault could not delete' "$TEST_ROOT/failure-403.err" >/dev/null
grep -F 'Vault DELETE request failed after 3 attempts (HTTP 502)' \
  "$TEST_ROOT/failure-502.err" >/dev/null

if run_preview_secrets ensure scribe/previews/scribe-prod@example-project.iam.gserviceaccount.com >/dev/null 2>&1; then
  echo "Preview bootstrap accepted a production prefix" >&2
  exit 1
fi

if run_preview_secrets ensure scribe/previews/scribe-pr-75@other.example.com >/dev/null 2>&1; then
  echo "Preview bootstrap accepted a non-GCP service-account namespace" >&2
  exit 1
fi

echo "Preview Vault bootstrap and teardown contracts passed."
