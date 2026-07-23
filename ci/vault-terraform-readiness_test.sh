#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d)"
trap 'rm -rf "$TEST_DIR"' EXIT

mkdir -p "$TEST_DIR/bin" "$TEST_DIR/backups"
terraform_log="$TEST_DIR/terraform.log"
curl_attempts="$TEST_DIR/curl-attempts"

cat >"$TEST_DIR/bin/terraform" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$TF_TEST_TERRAFORM_LOG"
if [[ "${1:-}" == init && "${TF_TEST_ECHO_VAULT_TOKEN_ON_INIT:-false}" == true ]]; then
  printf 'Terraform init inherited Vault token: %s\n' "$VAULT_TOKEN"
fi
case "${1:-}" in
  init|validate) exit 0 ;;
  workspace) exit 0 ;;
  plan)
    [ "$(cat "$TF_TEST_CURL_ATTEMPTS")" -ge 3 ] || {
      echo "Terraform plan ran before Vault token readiness" >&2
      exit 90
    }
    exit 0
    ;;
  *) echo "unexpected terraform invocation: $*" >&2; exit 2 ;;
esac
EOF

cat >"$TEST_DIR/bin/gcloud" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "${1:-} ${2:-}" in
  "run services")
    printf 'https://vault.example.test\n'
    ;;
  "auth print-access-token")
    printf '%s\n' "$TF_TEST_ADMIN_TOKEN"
    ;;
  "auth print-identity-token")
    printf 'test-google-id-token\n'
    ;;
  "config get-value")
    printf 'github@example-project.iam.gserviceaccount.com\n'
    ;;
  *) echo "unexpected gcloud invocation" >&2; exit 2 ;;
esac
EOF

cat >"$TEST_DIR/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
args=" $* "
if [[ "$args" == *" https://vault.example.test/v1/auth/google-jwt/login "* ]]; then
  printf '{"auth":{"client_token":"%s"}}\n' "$TF_TEST_JWT_TOKEN"
  exit 0
fi

attempt="$(cat "$TF_TEST_CURL_ATTEMPTS")"
attempt=$((attempt + 1))
printf '%s\n' "$attempt" >"$TF_TEST_CURL_ATTEMPTS"

[[ "$args" == *" --connect-timeout 5 "* ]] || exit 91
[[ "$args" == *" --max-time 10 "* ]] || exit 92
[[ "$args" == *" -o /dev/null "* ]] || exit 93
[[ "$args" == *" -H X-Vault-Token: ${VAULT_TOKEN} "* ]] || exit 94
[[ "$args" == *" -H X-Admin-Token: ${TF_TEST_ADMIN_TOKEN} "* ]] || exit 95
[[ "$args" == *" https://vault.example.test/v1/auth/token/lookup-self "* ]] || exit 96

# Simulate a verbose upstream failure. deploy-local.sh must discard both
# streams and expose only the stable retry label and attempt count.
printf '%s\n' "$TF_TEST_RESPONSE_SECRET"
printf '%s\n' "$TF_TEST_RESPONSE_SECRET" >&2
if [ "$TF_TEST_CURL_MODE" = "permanent" ] || [ "$attempt" -lt 3 ]; then
  exit 22
fi
EOF

cat >"$TEST_DIR/bin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = "symbolic-ref" ]; then
  printf 'main\n'
  exit 0
fi
echo "unexpected git invocation" >&2
exit 2
EOF

cat >"$TEST_DIR/bin/docker" <<'EOF'
#!/usr/bin/env bash
echo "Docker must not inspect a digest-pinned test image" >&2
exit 97
EOF

for command in terraform gcloud curl git docker; do
  chmod +x "$TEST_DIR/bin/$command"
done
for command in bash cat dirname jq mktemp rm sed awk tr sleep; do
  ln -s "$(command -v "$command")" "$TEST_DIR/bin/$command"
done

cat >"$TEST_DIR/backups/readiness-state.json" <<'EOF'
{
  "versioning": {"enabled": true},
  "softDeletePolicy": {"retentionDurationSeconds": "1209600s"}
}
EOF

vault_token='test-vault-token-do-not-log'
jwt_vault_token='test-jwt-vault-token-do-not-log'
admin_token='test-admin-token-do-not-log'
response_secret='test-vault-response-do-not-log'
digest="$(printf 'a%.0s' $(seq 1 64))"

run_deploy() {
  local mode="$1"
  local stdout_file="$2"
  local stderr_file="$3"

  printf '0\n' >"$curl_attempts"
  : >"$terraform_log"
  GITHUB_ACTIONS=false \
    PATH="$TEST_DIR/bin" \
    GCLOUD_PROJECT=example-project \
    TF_STATE_BUCKET=readiness-state \
    TF_TARGET_SET=vault-ci-identities \
    BACKUP_AUDIT_FIXTURE_DIR="$TEST_DIR/backups" \
    SCRIBE_API_IMAGE="ghcr.io/example/scribe@sha256:${digest}" \
    VAULT_TOKEN="$vault_token" \
    VAULT_RETRY_ATTEMPTS=4 \
    VAULT_RETRY_INITIAL_DELAY_SECONDS=0 \
    VAULT_RETRY_MAX_DELAY_SECONDS=0 \
    TF_TEST_ADMIN_TOKEN="$admin_token" \
    TF_TEST_JWT_TOKEN="$jwt_vault_token" \
    TF_TEST_RESPONSE_SECRET="$response_secret" \
    TF_TEST_CURL_MODE="$mode" \
    TF_TEST_CURL_ATTEMPTS="$curl_attempts" \
    TF_TEST_TERRAFORM_LOG="$terraform_log" \
    "$ROOT_DIR/terraform/deploy-local.sh" dev plan --branch main \
      >"$stdout_file" 2>"$stderr_file"
}

assert_redacted() {
  local output_file
  for output_file in "$@"; do
    if grep -F "$vault_token" "$output_file" >/dev/null \
      || grep -F "$admin_token" "$output_file" >/dev/null \
      || grep -F "$response_secret" "$output_file" >/dev/null; then
      echo "Vault readiness output exposed a token or response body" >&2
      exit 1
    fi
  done
}

run_deploy transient "$TEST_DIR/transient.out" "$TEST_DIR/transient.err"
[ "$(cat "$curl_attempts")" -eq 3 ] || {
  echo "Vault readiness did not recover on the third attempt" >&2
  exit 1
}
[ "$(grep -c 'Vault token readiness attempt .* failed; retrying.' "$TEST_DIR/transient.err")" -eq 2 ] || {
  echo "Vault readiness did not report two redacted transient retries" >&2
  exit 1
}
grep -F 'plan ' "$terraform_log" >/dev/null
assert_redacted "$TEST_DIR/transient.out" "$TEST_DIR/transient.err"

run_captured_deploy() {
  local token_source="$1"
  local expected_token="$2"
  local artifact="$TEST_DIR/${token_source}-artifact.log"
  local runner="$TEST_DIR/${token_source}-runner.log"
  local inherited_token="$vault_token"
  local echo_inherited_token=false

  if [[ "$token_source" == jwt ]]; then
    inherited_token=""
  else
    echo_inherited_token=true
  fi
  printf '0\n' >"$curl_attempts"
  : >"$terraform_log"
  GITHUB_ACTIONS=true \
    PATH="$TEST_DIR/bin" \
    GCLOUD_PROJECT=example-project \
    TF_STATE_BUCKET=readiness-state \
    TF_TARGET_SET=vault-ci-identities \
    BACKUP_AUDIT_FIXTURE_DIR="$TEST_DIR/backups" \
    SCRIBE_API_IMAGE="ghcr.io/example/scribe@sha256:${digest}" \
    VAULT_BOOTSTRAP_MODE=jwt \
    VAULT_TOKEN="$inherited_token" \
    VAULT_RETRY_ATTEMPTS=4 \
    VAULT_RETRY_INITIAL_DELAY_SECONDS=0 \
    VAULT_RETRY_MAX_DELAY_SECONDS=0 \
    TF_TEST_ADMIN_TOKEN="$admin_token" \
    TF_TEST_JWT_TOKEN="$jwt_vault_token" \
    TF_TEST_RESPONSE_SECRET="$response_secret" \
    TF_TEST_ECHO_VAULT_TOKEN_ON_INIT="$echo_inherited_token" \
    TF_TEST_CURL_MODE=transient \
    TF_TEST_CURL_ATTEMPTS="$curl_attempts" \
    TF_TEST_TERRAFORM_LOG="$terraform_log" \
    "$ROOT_DIR/ci/capture-command-log.sh" "$artifact" \
      "$ROOT_DIR/terraform/deploy-local.sh" dev plan --branch main >"$runner"

  [[ "$(cat "$curl_attempts")" -eq 3 ]] || {
    echo "Captured ${token_source} deployment did not complete Vault readiness" >&2
    exit 1
  }
  grep -Fx "::add-mask::${expected_token}" "$runner" >/dev/null || {
    echo "Captured ${token_source} deployment did not register its Vault token" >&2
    exit 1
  }
  if grep -F '::add-mask::' "$artifact" >/dev/null ||
    grep -F "$expected_token" "$artifact" >/dev/null; then
    echo "Captured ${token_source} deployment persisted its Vault token or mask record" >&2
    exit 1
  fi
  if [[ "$token_source" == inherited ]]; then
    grep -Fx 'Terraform init inherited Vault token: ***' "$artifact" >/dev/null || {
      echo "Captured inherited deployment did not redact the token before Terraform init" >&2
      exit 1
    }
  fi
}

run_captured_deploy inherited "$vault_token"
run_captured_deploy jwt "$jwt_vault_token"

if run_deploy permanent "$TEST_DIR/permanent.out" "$TEST_DIR/permanent.err"; then
  echo "Vault readiness accepted a permanently unavailable token endpoint" >&2
  exit 1
fi
[ "$(cat "$curl_attempts")" -eq 4 ] || {
  echo "Vault readiness did not stop at the configured attempt limit" >&2
  exit 1
}
if grep -F 'plan ' "$terraform_log" >/dev/null; then
  echo "Terraform planned after Vault readiness was exhausted" >&2
  exit 1
fi
grep -F 'Vault token readiness failed after 4 attempts.' "$TEST_DIR/permanent.err" >/dev/null
grep -F 'response body withheld.' "$TEST_DIR/permanent.err" >/dev/null
assert_redacted "$TEST_DIR/permanent.out" "$TEST_DIR/permanent.err"

echo "Terraform Vault token readiness and redaction contracts passed."
