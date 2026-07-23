#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/scribe-vault-init-diagnostics-test.XXXXXX")"
trap 'rm -rf -- "$TEMP_DIR"' EXIT
mkdir -p "$TEMP_DIR/bin"

fail() {
  echo "Vault init diagnostics contract failed: $*" >&2
  exit 1
}

REAL_CHOWN="$(command -v chown)"
REAL_MV="$(command -v mv)"
REAL_MKTEMP="$(command -v mktemp)"
REAL_OPENSSL="$(command -v openssl)"
readonly REAL_CHOWN REAL_MV REAL_MKTEMP REAL_OPENSSL
readonly TRACE_PATTERN='^SCRIBE_APP_INIT_TRACE_V1 stage=(vault-script-start|vault-helper-unavailable|vault-helper-ready|vault-settings-invalid|vault-settings-ready|vault-credentials-missing|vault-credentials-invalid|vault-credentials-ready|vault-jwt-failed|vault-jwt-ready|vault-login-start|vault-login-failed|vault-login-complete|vault-database-read-start|vault-database-read-failed|vault-database-value-missing|vault-database-read-complete|vault-output-write-start|vault-output-write-failed|vault-output-write-complete)$'

# shellcheck disable=SC2016 # These literals create a mock evaluated by its own Bash process.
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'set -euo pipefail' \
  'response_file=""' \
  'endpoint=""' \
  'while (($# > 0)); do' \
  '  case "$1" in' \
  '    -o) response_file="$2"; shift 2 ;;' \
  '    -H | -d | -w | -X | --connect-timeout | --max-time) shift 2 ;;' \
  '    -fsS | -sS) shift ;;' \
  '    *) endpoint="$1"; shift ;;' \
  '  esac' \
  'done' \
  '[[ -n "$response_file" ]] || exit 90' \
  'case "$endpoint" in' \
  '  */v1/auth/gcp/login)' \
  '    if [[ "$MOCK_CURL_MODE" == login-failed ]]; then' \
  '      printf "%s" '\''{"errors":["LOGIN_RESPONSE_SECRET_SENTINEL"]}'\'' >"$response_file"' \
  '      printf "503"' \
  '      exit 0' \
  '    fi' \
  '    printf "%s" '\''{"auth":{"client_token":"LOGIN_TOKEN_SECRET_SENTINEL"}}'\'' >"$response_file"' \
  '    printf "200"' \
  '    ;;' \
  '  */v1/*/data/*)' \
  '    case "$MOCK_CURL_MODE" in' \
  '      database-failed)' \
  '        printf "%s" '\''{"errors":["DATABASE_RESPONSE_SECRET_SENTINEL"]}'\'' >"$response_file"' \
  '        printf "503"' \
  '        exit 22' \
  '        ;;' \
  '      value-missing)' \
  '        printf "%s" '\''{"data":{"data":{"other":"DATABASE_RESPONSE_SECRET_SENTINEL"}}}'\'' >"$response_file"' \
  '        ;;' \
  '      *)' \
  '        printf "%s" '\''{"data":{"data":{"password":"DATABASE_PASSWORD_SECRET_SENTINEL"}}}'\'' >"$response_file"' \
  '        ;;' \
  '    esac' \
  '    printf "200"' \
  '    ;;' \
  '  *) exit 91 ;;' \
  'esac' >"$TEMP_DIR/bin/curl"
chmod +x "$TEMP_DIR/bin/curl"

# shellcheck disable=SC2016 # These literals create a mock evaluated by its own Bash process.
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'set -euo pipefail' \
  'if [[ "${MOCK_OPENSSL_FAIL:-none}" == "${1:-}" ]]; then' \
  '  exit 51' \
  'fi' \
  'exec "$REAL_OPENSSL" "$@"' >"$TEMP_DIR/bin/openssl"
chmod +x "$TEMP_DIR/bin/openssl"

# shellcheck disable=SC2016 # These literals create a mock evaluated by its own Bash process.
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'set -euo pipefail' \
  'count=0' \
  'if [[ -s "$MOCK_MKTEMP_STATE" ]]; then' \
  '  read -r count <"$MOCK_MKTEMP_STATE"' \
  'fi' \
  'count=$((count + 1))' \
  'printf "%s\n" "$count" >"$MOCK_MKTEMP_STATE"' \
  'if [[ "$count" == "${MOCK_MKTEMP_FAIL_CALL:-0}" ]]; then' \
  '  exit 53' \
  'fi' \
  'path="$("$REAL_MKTEMP" "$@")"' \
  'printf "%s\n" "$path" >>"$MOCK_MKTEMP_PATHS"' \
  'printf "%s\n" "$path"' >"$TEMP_DIR/bin/mktemp"
chmod +x "$TEMP_DIR/bin/mktemp"

# shellcheck disable=SC2016 # These literals create a mock evaluated by its own Bash process.
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'set -euo pipefail' \
  'if [[ "${MOCK_CHOWN_PAUSE:-false}" == true ]]; then' \
  '  : >"$MOCK_CHOWN_READY"' \
  '  while [[ ! -e "$MOCK_CHOWN_RELEASE" ]]; do' \
  '    sleep 0.01' \
  '  done' \
  '  exit 0' \
  'fi' \
  'exec "$REAL_CHOWN" "$@"' >"$TEMP_DIR/bin/chown"
chmod +x "$TEMP_DIR/bin/chown"

# shellcheck disable=SC2016 # These literals create a mock evaluated by its own Bash process.
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'set -euo pipefail' \
  'if [[ "${MOCK_MV_FAIL:-false}" == true ]]; then' \
  '  exit 52' \
  'fi' \
  'exec "$REAL_MV" "$@"' >"$TEMP_DIR/bin/mv"
chmod +x "$TEMP_DIR/bin/mv"

credential_key="$TEMP_DIR/credential-key.pem"
valid_credentials="$TEMP_DIR/valid-credentials.json"
invalid_credentials="$TEMP_DIR/invalid-credentials.json"
"$REAL_OPENSSL" genpkey -quiet -algorithm RSA \
  -pkeyopt rsa_keygen_bits:2048 -out "$credential_key"
jq -n \
  --arg client_email 'credential-sentinel@example.invalid' \
  --arg private_key_id 'CREDENTIAL_KEY_ID_SECRET_SENTINEL' \
  --rawfile private_key "$credential_key" \
  '{client_email: $client_email, private_key_id: $private_key_id, private_key: $private_key}' \
  >"$valid_credentials"
printf '%s\n' '{"client_email":"INVALID_CREDENTIAL_SECRET_SENTINEL"' >"$invalid_credentials"

observed_stages="$TEMP_DIR/observed-stages.log"
: >"$observed_stages"

run_case() {
  local name="$1" expected_status="$2" expected_final_stage="$3"
  shift 3
  local log="$TEMP_DIR/${name}.log"
  local marker_log="$TEMP_DIR/${name}-markers.log"
  local out_dir="$TEMP_DIR/out-${name}"
  local actual_status

  set +e
  env \
    PATH="$TEMP_DIR/bin:$PATH" \
    REAL_CHOWN="$REAL_CHOWN" \
    REAL_MV="$REAL_MV" \
    REAL_MKTEMP="$REAL_MKTEMP" \
    REAL_OPENSSL="$REAL_OPENSSL" \
    VAULT_RETRY_LIB="$ROOT_DIR/scripts/vault-retry.sh" \
    VAULT_RETRY_ATTEMPTS=1 \
    VAULT_RETRY_INITIAL_DELAY_SECONDS=0 \
    VAULT_RETRY_MAX_DELAY_SECONDS=0 \
    VAULT_ADDRESS=https://vault.example.invalid \
    VAULT_GCP_AUTH_ROLE=scribe-app-prod \
    GOOGLE_APPLICATION_CREDENTIALS="$valid_credentials" \
    OUT_DIR="$out_dir" \
    MOCK_CURL_MODE=success \
    MOCK_CHOWN_PAUSE=false \
    MOCK_OPENSSL_FAIL=none \
    MOCK_MV_FAIL=false \
    MOCK_MKTEMP_FAIL_CALL=0 \
    MOCK_MKTEMP_PATHS="$TEMP_DIR/${name}-mktemp-paths" \
    MOCK_MKTEMP_STATE="$TEMP_DIR/${name}-mktemp-count" \
    "$@" \
    /bin/sh "$ROOT_DIR/scripts/vault-init.sh" >"$log" 2>&1
  actual_status=$?
  set -e

  [[ "$actual_status" == "$expected_status" ]] ||
    fail "$name returned $actual_status instead of $expected_status"
  grep '^SCRIBE_APP_INIT_TRACE_V1 ' "$log" >"$marker_log" ||
    fail "$name emitted no app-init trace marker"
  if rg -v "$TRACE_PATTERN" "$marker_log" >/dev/null; then
    fail "$name emitted a non-allowlisted app-init trace marker"
  fi
  [[ "$(tail -n 1 "$marker_log")" == \
    "SCRIBE_APP_INIT_TRACE_V1 stage=${expected_final_stage}" ]] ||
    fail "$name did not finish at the expected fixed stage ${expected_final_stage}"
  sed -E 's/^SCRIBE_APP_INIT_TRACE_V1 stage=//' "$marker_log" >>"$observed_stages"

  if rg -n \
    'LOGIN_(RESPONSE|TOKEN)_SECRET_SENTINEL|DATABASE_(RESPONSE|PASSWORD)_SECRET_SENTINEL|credential-sentinel|CREDENTIAL_KEY_ID_SECRET_SENTINEL|INVALID_CREDENTIAL_SECRET_SENTINEL' \
    "$log" >/dev/null; then
    fail "$name exposed a credential, JWT input, Vault token, password, or response body"
  fi
  if [[ -e "$TEMP_DIR/${name}-mktemp-paths" ]]; then
    while IFS= read -r temp_path; do
      [[ ! -e "$temp_path" ]] ||
        fail "$name left a credential or response temporary file behind"
    done <"$TEMP_DIR/${name}-mktemp-paths"
  fi
}

run_case helper-unavailable 1 vault-helper-unavailable \
  VAULT_RETRY_LIB="$TEMP_DIR/unavailable-vault-retry.sh"
printf '%s\n' 'vault_retry() {' >"$TEMP_DIR/invalid-vault-retry.sh"
run_case helper-invalid 1 vault-helper-unavailable \
  VAULT_RETRY_LIB="$TEMP_DIR/invalid-vault-retry.sh"
run_case settings-invalid 1 vault-settings-invalid \
  VAULT_ADDRESS=
run_case credentials-missing 1 vault-credentials-missing \
  GOOGLE_APPLICATION_CREDENTIALS="$TEMP_DIR/missing-credentials.json"
run_case credentials-invalid 1 vault-credentials-invalid \
  GOOGLE_APPLICATION_CREDENTIALS="$invalid_credentials"
run_case jwt-base64-failed 1 vault-jwt-failed \
  MOCK_OPENSSL_FAIL=base64
run_case jwt-signature-failed 1 vault-jwt-failed \
  MOCK_OPENSSL_FAIL=dgst
run_case login-temp-failed 1 vault-login-failed \
  MOCK_MKTEMP_FAIL_CALL=3
run_case login-failed 1 vault-login-failed \
  MOCK_CURL_MODE=login-failed
run_case database-temp-failed 1 vault-database-read-failed \
  MOCK_MKTEMP_FAIL_CALL=5
run_case database-failed 1 vault-database-read-failed \
  MOCK_CURL_MODE=database-failed
run_case value-missing 1 vault-database-value-missing \
  MOCK_CURL_MODE=value-missing
run_case output-failed 1 vault-output-write-failed \
  MOCK_MV_FAIL=true
run_case success 0 vault-output-write-complete

signal_log="$TEMP_DIR/output-signal.log"
signal_out_dir="$TEMP_DIR/out-output-signal"
signal_paths="$TEMP_DIR/output-signal-mktemp-paths"
signal_state="$TEMP_DIR/output-signal-mktemp-count"
signal_ready="$TEMP_DIR/output-signal-chown-ready"
signal_release="$TEMP_DIR/output-signal-chown-release"
env \
  PATH="$TEMP_DIR/bin:$PATH" \
  REAL_CHOWN="$REAL_CHOWN" \
  REAL_MV="$REAL_MV" \
  REAL_MKTEMP="$REAL_MKTEMP" \
  REAL_OPENSSL="$REAL_OPENSSL" \
  VAULT_RETRY_LIB="$ROOT_DIR/scripts/vault-retry.sh" \
  VAULT_RETRY_ATTEMPTS=1 \
  VAULT_RETRY_INITIAL_DELAY_SECONDS=0 \
  VAULT_RETRY_MAX_DELAY_SECONDS=0 \
  VAULT_ADDRESS=https://vault.example.invalid \
  VAULT_GCP_AUTH_ROLE=scribe-app-prod \
  GOOGLE_APPLICATION_CREDENTIALS="$valid_credentials" \
  HOST_UID=1000 \
  HOST_GID=1000 \
  OUT_DIR="$signal_out_dir" \
  MOCK_CHOWN_PAUSE=true \
  MOCK_CHOWN_READY="$signal_ready" \
  MOCK_CHOWN_RELEASE="$signal_release" \
  MOCK_CURL_MODE=success \
  MOCK_MKTEMP_FAIL_CALL=0 \
  MOCK_MKTEMP_PATHS="$signal_paths" \
  MOCK_MKTEMP_STATE="$signal_state" \
  MOCK_MV_FAIL=false \
  MOCK_OPENSSL_FAIL=none \
  /bin/sh "$ROOT_DIR/scripts/vault-init.sh" >"$signal_log" 2>&1 &
signal_pid=$!

for _ in {1..200}; do
  [[ -e "$signal_ready" ]] && break
  sleep 0.01
done
[[ -e "$signal_ready" ]] ||
  fail "the signal fixture did not pause after writing the output credential"
signal_temp_path="$(tail -n 1 "$signal_paths")"
[[ -f "$signal_temp_path" ]] ||
  fail "the signal fixture did not create the output credential temporary file"
kill -TERM "$signal_pid"
: >"$signal_release"
set +e
wait "$signal_pid"
signal_status=$?
set -e
[[ "$signal_status" -ne 0 ]] ||
  fail "the signal fixture unexpectedly completed successfully"
[[ ! -e "$signal_temp_path" ]] ||
  fail "SIGTERM left the output credential temporary file behind"
[[ ! -e "$signal_out_dir/mariadb_password" ]] ||
  fail "SIGTERM promoted the interrupted output credential"
if grep -Eq 'SCRIBE_APP_INIT_TRACE_V1 stage=vault-output-write-(failed|complete)' "$signal_log"; then
  fail "Vault init resumed credential processing after SIGTERM"
fi
[[ "$(grep '^SCRIBE_APP_INIT_TRACE_V1 ' "$signal_log" | tail -n 1)" == \
  'SCRIBE_APP_INIT_TRACE_V1 stage=vault-output-write-start' ]] ||
  fail "SIGTERM did not stop Vault init at the interrupted output stage"
if rg -n 'DATABASE_PASSWORD_SECRET_SENTINEL' "$signal_log" >/dev/null; then
  fail "the signal fixture exposed the database password"
fi
grep '^SCRIBE_APP_INIT_TRACE_V1 ' "$signal_log" |
  while IFS= read -r marker; do
    [[ "$marker" =~ $TRACE_PATTERN ]] ||
      fail "the signal fixture emitted a non-allowlisted app-init trace marker"
  done

[[ "$(cat "$TEMP_DIR/out-success/mariadb_password")" == \
  'DATABASE_PASSWORD_SECRET_SENTINEL' ]] ||
  fail "the successful fixture did not materialize the expected database credential"

expected_success_markers="$(
  printf '%s\n' \
    'SCRIBE_APP_INIT_TRACE_V1 stage=vault-script-start' \
    'SCRIBE_APP_INIT_TRACE_V1 stage=vault-helper-ready' \
    'SCRIBE_APP_INIT_TRACE_V1 stage=vault-settings-ready' \
    'SCRIBE_APP_INIT_TRACE_V1 stage=vault-credentials-ready' \
    'SCRIBE_APP_INIT_TRACE_V1 stage=vault-jwt-ready' \
    'SCRIBE_APP_INIT_TRACE_V1 stage=vault-login-start' \
    'SCRIBE_APP_INIT_TRACE_V1 stage=vault-login-complete' \
    'SCRIBE_APP_INIT_TRACE_V1 stage=vault-database-read-start' \
    'SCRIBE_APP_INIT_TRACE_V1 stage=vault-database-read-complete' \
    'SCRIBE_APP_INIT_TRACE_V1 stage=vault-output-write-start' \
    'SCRIBE_APP_INIT_TRACE_V1 stage=vault-output-write-complete'
)"
[[ "$(cat "$TEMP_DIR/success-markers.log")" == "$expected_success_markers" ]] ||
  fail "the successful Vault-init stage sequence changed"

expected_stages="$(
  printf '%s\n' \
    vault-credentials-invalid \
    vault-credentials-missing \
    vault-credentials-ready \
    vault-database-read-complete \
    vault-database-read-failed \
    vault-database-read-start \
    vault-database-value-missing \
    vault-helper-ready \
    vault-helper-unavailable \
    vault-jwt-failed \
    vault-jwt-ready \
    vault-login-complete \
    vault-login-failed \
    vault-login-start \
    vault-output-write-complete \
    vault-output-write-failed \
    vault-output-write-start \
    vault-script-start \
    vault-settings-invalid \
    vault-settings-ready
)"
[[ "$(sort -u "$observed_stages")" == "$expected_stages" ]] ||
  fail "the failure matrix did not exercise the complete Vault-init stage enum"

echo "Vault init emits only fixed stage diagnostics and withholds credentials, response bodies, tokens, and passwords."
