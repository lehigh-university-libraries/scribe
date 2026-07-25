#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d)"
trap 'rm -rf "$TEST_DIR"' EXIT

mkdir -p "$TEST_DIR/bin" "$TEST_DIR/backups"
terraform_log="$TEST_DIR/terraform.log"
curl_attempts="$TEST_DIR/curl-attempts"
go_log="$TEST_DIR/go.log"
reconciler_log="$TEST_DIR/reconciler.log"

cat >"$TEST_DIR/bin/terraform" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$TF_TEST_TERRAFORM_LOG"
if [[ "${1:-}" == init && "${TF_TEST_ECHO_VAULT_TOKEN_ON_INIT:-false}" == true ]]; then
  printf 'Terraform init inherited Vault token: %s\n' "$VAULT_TOKEN"
fi
if [ "${TF_TEST_PREVIEW_RUNTIME:-false}" = true ]; then
  [ -z "${VAULT_TOKEN+x}" ] && [ -z "${VAULT_ADDR+x}" ] || {
    echo "Terraform inherited Vault credentials during preview-runtime reconciliation" >&2
    exit 2
  }
fi
case "${1:-}" in
  init|validate) exit 0 ;;
  workspace) exit 0 ;;
  state)
    [ "${TF_TEST_PREVIEW_RUNTIME:-false}" = true ] || {
      echo "unexpected terraform state invocation: $*" >&2
      exit 2
    }
    [ "${2:-}" = list ] || {
      echo "unexpected terraform state invocation: $*" >&2
      exit 2
    }
    printf '%s\n' 'module.vault[0].google_cloud_run_v2_service.vault'
    exit 0
    ;;
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

assert_no_vault_credentials() {
  [ -z "${VAULT_TOKEN+x}" ] && [ -z "${VAULT_ADDR+x}" ] || {
    echo "gcloud inherited Vault credentials" >&2
    exit 2
  }
}

case "${1:-} ${2:-}" in
  "projects describe")
    [ "$*" = "projects describe example-project --format=json" ] || {
      echo "unexpected project lookup: $*" >&2
      exit 2
    }
    assert_no_vault_credentials
    jq -cn \
      --arg project_id "${TF_TEST_PROJECT_ID:-example-project}" \
      --arg project_number "${TF_TEST_PROJECT_NUMBER:-123456789012}" \
      '{projectId: $project_id, projectNumber: $project_number}'
    ;;
  "run services")
    [ "$*" = "run services describe vault-server-dev --project example-project --region us-east5 --format=json" ] || {
      echo "unexpected Vault service lookup: $*" >&2
      exit 2
    }
    assert_no_vault_credentials
    jq -cn \
      --arg service_name "${TF_TEST_VAULT_SERVICE_NAME:-vault-server-dev}" \
      --arg service_url "${TF_TEST_VAULT_STATUS_URL:-https://vault-server-dev-legacy-hash-ue.a.run.app}" \
      --arg service_account "${TF_TEST_VAULT_GSA:-vault-server-dev@example-project.iam.gserviceaccount.com}" \
      '{
        metadata: {name: $service_name},
        status: {url: $service_url},
        spec: {template: {spec: {serviceAccountName: $service_account}}}
      }'
    ;;
  "storage cp")
    [ "${TF_TEST_PREVIEW_RUNTIME:-false}" = true ] || {
      echo "unexpected root-token download: $*" >&2
      exit 2
    }
    [ "${3:-}" = "gs://example-project-vault-server-dev-key/root-token.enc" ] || {
      echo "unexpected root-token object: ${3:-}" >&2
      exit 2
    }
    printf '%s\n' "$TF_TEST_PREVIEW_ROOT_TOKEN" | base64 >"${4:?missing root-token destination}"
    ;;
  "kms decrypt")
    [ "${TF_TEST_PREVIEW_RUNTIME:-false}" = true ] || {
      echo "unexpected root-token decryption: $*" >&2
      exit 2
    }
    ciphertext=""
    plaintext=""
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --ciphertext-file) ciphertext="$2"; shift 2 ;;
        --plaintext-file) plaintext="$2"; shift 2 ;;
        *) shift ;;
      esac
    done
    [ -n "$ciphertext" ] && [ -n "$plaintext" ] || {
      echo "root-token decrypt paths were incomplete" >&2
      exit 2
    }
    cp "$ciphertext" "$plaintext"
    ;;
  "auth print-access-token")
    assert_no_vault_credentials
    printf '%s\n' "$TF_TEST_ADMIN_TOKEN"
    ;;
  "auth print-identity-token")
    [ "${TF_TEST_PREVIEW_RUNTIME:-false}" != true ] || exit 2
    printf 'test-google-id-token\n'
    ;;
  "config get-value")
    [ "${TF_TEST_PREVIEW_RUNTIME:-false}" != true ] || exit 2
    printf 'github@example-project.iam.gserviceaccount.com\n'
    ;;
  *) echo "unexpected gcloud invocation: $*" >&2; exit 2 ;;
esac
EOF

cat >"$TEST_DIR/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
args=" $* "
if [ "${TF_TEST_PREVIEW_RUNTIME:-false}" = true ]; then
  [ "${VAULT_TOKEN:-}" = "$TF_TEST_PREVIEW_ROOT_TOKEN" ] || exit 90
  [[ "$args" == *" -H X-Vault-Token: ${TF_TEST_PREVIEW_ROOT_TOKEN} "* ]] || exit 91
  [[ "$args" == *" -H X-Admin-Token: ${TF_TEST_ADMIN_TOKEN} "* ]] || exit 92
  [[ "$args" == *" https://vault-server-dev-123456789012.us-east5.run.app/v1/auth/token/lookup-self "* ]] || exit 93
  exit 0
fi
if [[ "$args" == *" https://vault-server-dev-123456789012.us-east5.run.app/v1/auth/google-jwt/login "* ]]; then
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
[[ "$args" == *" https://vault-server-dev-123456789012.us-east5.run.app/v1/auth/token/lookup-self "* ]] || exit 96

# Simulate a verbose upstream failure. deploy-local.sh must discard both
# streams and expose only the stable retry label and attempt count.
printf '%s\n' "$TF_TEST_RESPONSE_SECRET"
printf '%s\n' "$TF_TEST_RESPONSE_SECRET" >&2
if [ "$TF_TEST_CURL_MODE" = "permanent" ] || [ "$attempt" -lt 3 ]; then
  exit 22
fi
EOF

cat >"$TEST_DIR/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[ -z "${VAULT_TOKEN+x}" ] && [ -z "${VAULT_ADDR+x}" ] || {
  echo "Go toolchain inherited Vault credentials" >&2
  exit 2
}
printf '%s\n' "$*" >>"$TF_TEST_GO_LOG"
if [ "${1:-}" = env ] && [ "${2:-}" = GOVERSION ]; then
  printf 'go%s\n' "$(tr -d '[:space:]' <"$TF_TEST_GO_VERSION_FILE")"
  exit 0
fi
if [ "${1:-}" = build ]; then
  output=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
      -o) output="$2"; shift 2 ;;
      *) shift ;;
    esac
  done
  [ -n "$output" ] || {
    echo "go build did not provide an output path" >&2
    exit 2
  }
  cp "$TF_TEST_RECONCILER_FIXTURE" "$output"
  chmod +x "$output"
  exit 0
fi
echo "unexpected go invocation: $*" >&2
exit 2
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

cat >"$TEST_DIR/fake-preview-reconciler" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[ "$*" = "-mode=apply" ] || {
  echo "unexpected preview reconciler mode: $*" >&2
  exit 2
}
[ "${VAULT_ADDR:-}" = "https://vault-server-dev-123456789012.us-east5.run.app" ] || {
  echo "preview reconciler received the wrong Vault address" >&2
  exit 2
}
[ "${VAULT_TOKEN:-}" = "$TF_TEST_PREVIEW_ROOT_TOKEN" ] || {
  echo "preview reconciler did not receive the recovered root token" >&2
  exit 2
}
[ "${GCLOUD_PROJECT:-}" = "example-project" ] || {
  echo "preview reconciler received the wrong project ID" >&2
  exit 2
}
[ "${GCLOUD_PROJECT_NUMBER:-}" = "123456789012" ] || {
  echo "preview reconciler received the wrong independently resolved project number" >&2
  exit 2
}
printf '%s\n' "mode=apply" >"$TF_TEST_RECONCILER_LOG"
EOF

for command in terraform gcloud curl go git docker; do
  chmod +x "$TEST_DIR/bin/$command"
done
chmod +x "$TEST_DIR/fake-preview-reconciler"
for command in bash base64 cat chmod cp dirname env find grep jq mktemp rm sed awk tr sleep; do
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
preview_root_token='test-preview-root-token-do-not-log'
digest="$(printf 'a%.0s' $(seq 1 64))"

vault_resolution="$(
  env -u VAULT_TOKEN -u VAULT_ADDR \
    PATH="$TEST_DIR/bin" \
    GCLOUD_PROJECT=example-project \
    SCRIBE_REGION=us-east5 \
    TF_TEST_PROJECT_ID=example-project \
    TF_TEST_PROJECT_NUMBER=123456789012 \
    TF_TEST_VAULT_GSA=vault-server-dev@example-project.iam.gserviceaccount.com \
    TF_TEST_VAULT_SERVICE_NAME=vault-server-dev \
    TF_TEST_VAULT_STATUS_URL=https://vault-server-dev-legacy-hash-ue.a.run.app \
    "$ROOT_DIR/ci/resolve-shared-vault.sh" dev
)"
jq -e '
  . == {
    vault_addr: "https://vault-server-dev-123456789012.us-east5.run.app",
    vault_audience: "https://vault-server-dev-legacy-hash-ue.a.run.app",
    project_number: "123456789012",
    service_account: "vault-server-dev@example-project.iam.gserviceaccount.com"
  }
' <<<"$vault_resolution" >/dev/null || {
  echo "Shared Vault resolution did not preserve the Terraform-owned JWT audience" >&2
  exit 1
}

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
    TF_TEST_VAULT_GSA="${TF_TEST_VAULT_GSA:-vault-server-dev@example-project.iam.gserviceaccount.com}" \
    "$ROOT_DIR/terraform/deploy-local.sh" dev plan --branch main \
      >"$stdout_file" 2>"$stderr_file"
}

run_preview_runtime() {
  local project_id="$1"
  local project_number="$2"
  local runtime_gsa="$3"
  local stdout_file="$4"
  local stderr_file="$5"
  local service_name="${6:-vault-server-dev}"
  local status_url="${7:-https://vault-server-dev-legacy-hash-ue.a.run.app}"

  printf '0\n' >"$curl_attempts"
  : >"$terraform_log"
  : >"$go_log"
  : >"$reconciler_log"
  if ! env -u VAULT_TOKEN -u VAULT_ADDR \
    GITHUB_ACTIONS=false \
    PATH="$TEST_DIR/bin" \
    GCLOUD_PROJECT=example-project \
    GCLOUD_PROJECT_NUMBER=999999999999 \
    SCRIBE_REGION=us-east5 \
    TF_STATE_BUCKET=readiness-state \
    TF_TARGET_SET=vault-preview-runtime \
    BACKUP_AUDIT_FIXTURE_DIR="$TEST_DIR/backups" \
    VAULT_BOOTSTRAP_MODE=root-token \
    VAULT_RETRY_ATTEMPTS=1 \
    TF_TEST_ADMIN_TOKEN="$admin_token" \
    TF_TEST_PREVIEW_ROOT_TOKEN="$preview_root_token" \
    TF_TEST_PREVIEW_RUNTIME=true \
    TF_TEST_PROJECT_ID="$project_id" \
    TF_TEST_PROJECT_NUMBER="$project_number" \
    TF_TEST_VAULT_GSA="$runtime_gsa" \
    TF_TEST_VAULT_SERVICE_NAME="$service_name" \
    TF_TEST_VAULT_STATUS_URL="$status_url" \
    TF_TEST_GO_LOG="$go_log" \
    TF_TEST_GO_VERSION_FILE="$ROOT_DIR/.go-version" \
    TF_TEST_RECONCILER_FIXTURE="$TEST_DIR/fake-preview-reconciler" \
    TF_TEST_RECONCILER_LOG="$reconciler_log" \
    TF_TEST_TERRAFORM_LOG="$terraform_log" \
    "$ROOT_DIR/terraform/deploy-local.sh" dev apply --branch main \
      >"$stdout_file" 2>"$stderr_file"; then
    return 1
  fi
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

TF_TEST_VAULT_GSA=other@example-project.iam.gserviceaccount.com \
  run_deploy transient "$TEST_DIR/owner-repair.out" "$TEST_DIR/owner-repair.err"
grep -F 'plan ' "$terraform_log" >/dev/null || {
  echo "Owner maintenance could not plan a repair for Vault runtime identity drift" >&2
  exit 1
}
assert_redacted "$TEST_DIR/owner-repair.out" "$TEST_DIR/owner-repair.err"

run_preview_runtime \
  example-project \
  123456789012 \
  vault-server-dev@example-project.iam.gserviceaccount.com \
  "$TEST_DIR/preview-runtime.out" \
  "$TEST_DIR/preview-runtime.err"
grep -Fx 'mode=apply' "$reconciler_log" >/dev/null || {
  echo "Preview Vault runtime reconciliation did not execute in apply mode" >&2
  exit 1
}
[ "$(grep -c '^state list$' "$terraform_log")" -eq 1 ] || {
  echo "Preview Vault runtime reconciliation did not verify the existing owner state" >&2
  exit 1
}
if grep -Eq '^(validate|plan|apply)([[:space:]]|$)|-target=' "$terraform_log"; then
  echo "Preview Vault runtime reconciliation traversed an unrelated Terraform graph" >&2
  exit 1
fi
grep -Fx 'env GOVERSION' "$go_log" >/dev/null || {
  echo "Preview Vault runtime reconciliation skipped the shared Go toolchain check" >&2
  exit 1
}
grep -Eq '^build -trimpath -o .+ \./cmd/vault-preview-runtime$' "$go_log" || {
  echo "Preview Vault runtime reconciliation did not build the typed command" >&2
  exit 1
}
if grep -F "$preview_root_token" "$TEST_DIR/preview-runtime.out" "$TEST_DIR/preview-runtime.err" >/dev/null; then
  echo "Preview Vault runtime reconciliation exposed its recovered root token" >&2
  exit 1
fi

for invalid_case in wrong-project wrong-number wrong-runtime-identity wrong-service-name wrong-service-url; do
  project_id=example-project
  project_number=123456789012
  runtime_gsa=vault-server-dev@example-project.iam.gserviceaccount.com
  service_name=vault-server-dev
  status_url=https://vault-server-dev-legacy-hash-ue.a.run.app
  expected_error=""
  case "$invalid_case" in
    wrong-project)
      project_id=other-project
      expected_error='The resolved Google Cloud project identity did not match GCLOUD_PROJECT.'
      ;;
    wrong-number)
      project_number=not-a-number
      expected_error='The resolved Google Cloud project number was invalid.'
      ;;
    wrong-runtime-identity)
      runtime_gsa=other@example-project.iam.gserviceaccount.com
      expected_error='The shared Vault service does not use its expected runtime identity.'
      ;;
    wrong-service-name)
      service_name=other-service
      expected_error='The resolved shared Vault service identity was unexpected.'
      ;;
    wrong-service-url)
      status_url=https://example.invalid/vault-server-dev.run.app
      expected_error='The shared Vault service did not expose a valid default HTTPS origin.'
      ;;
  esac
  if run_preview_runtime \
    "$project_id" \
    "$project_number" \
    "$runtime_gsa" \
    "$TEST_DIR/${invalid_case}.out" \
    "$TEST_DIR/${invalid_case}.err" \
    "$service_name" \
    "$status_url"; then
    echo "Preview Vault runtime reconciliation accepted ${invalid_case}" >&2
    exit 1
  fi
  grep -F "$expected_error" "$TEST_DIR/${invalid_case}.err" >/dev/null || {
    echo "Preview Vault runtime reconciliation did not fail ${invalid_case} explicitly" >&2
    exit 1
  }
  [ ! -s "$reconciler_log" ] || {
    echo "Preview Vault runtime reconciler ran after rejecting ${invalid_case}" >&2
    exit 1
  }
  if grep -Eq '^(validate|plan|apply)([[:space:]]|$)|-target=' "$terraform_log"; then
    echo "Preview Vault runtime rejection traversed Terraform for ${invalid_case}" >&2
    exit 1
  fi
done

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
