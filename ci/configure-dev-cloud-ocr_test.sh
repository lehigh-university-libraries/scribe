#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d -- "${TMPDIR:-/tmp}/scribe-cloud-ocr-test.XXXXXXXXXX")"
cleanup() {
  rm -rf -- "${TEST_DIR}"
}
trap cleanup EXIT

mkdir -p -- "${TEST_DIR}/project/scripts" "${TEST_DIR}/project/ci" "${TEST_DIR}/bin"
cp -- "${ROOT_DIR}/scripts/configure-dev-cloud-ocr.sh" "${TEST_DIR}/project/scripts/"
cp -- "${ROOT_DIR}/ci/validate-dev-cloud-ocr-credential.sh" "${TEST_DIR}/project/ci/"

cat >"${TEST_DIR}/bin/gcloud" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

case "$*" in
  "auth application-default login"*)
    target=""
    for argument in "$@"; do
      case "${argument}" in
        --impersonate-service-account=*) target="${argument#*=}" ;;
      esac
    done
    [ -n "${target}" ]
    count=0
    [ ! -f "${FAKE_GCLOUD_STATE}/login-count" ] || count="$(<"${FAKE_GCLOUD_STATE}/login-count")"
    count=$((count + 1))
    printf '%s' "${count}" >"${FAKE_GCLOUD_STATE}/login-count"
    mkdir -p -- "${CLOUDSDK_CONFIG}"
    jq -n --arg target "${target}" --arg refresh "refresh-${count}" '{
      type: "impersonated_service_account",
      service_account_impersonation_url:
        ("https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/" + $target + ":generateAccessToken"),
      source_credentials: {
        type: "authorized_user",
        client_id: "client-id",
        client_secret: "client-secret",
        refresh_token: $refresh,
        universe_domain: "googleapis.com",
        account: "developer@example.com"
      }
    }' >"${CLOUDSDK_CONFIG}/application_default_credentials.json"
    ;;
  "auth application-default revoke"*)
    [ -f "${CLOUDSDK_CONFIG}/application_default_credentials.json" ]
    refresh="$(jq -r '.source_credentials.refresh_token' "${CLOUDSDK_CONFIG}/application_default_credentials.json")"
    printf '%s\n' "${refresh}" >>"${FAKE_GCLOUD_STATE}/revoke-attempt.log"
    if [ -f "${FAKE_GCLOUD_STATE}/fail-next-revoke" ]; then
      rm -f -- "${FAKE_GCLOUD_STATE}/fail-next-revoke"
      exit 1
    fi
    printf '%s\n' "${refresh}" >>"${FAKE_GCLOUD_STATE}/revoke.log"
    rm -f -- "${CLOUDSDK_CONFIG}/application_default_credentials.json"
    ;;
  *)
    echo "unexpected fake gcloud invocation: $*" >&2
    exit 2
    ;;
esac
EOF
chmod 0755 -- "${TEST_DIR}/bin/gcloud"

export PATH="${TEST_DIR}/bin:${PATH}"
export FAKE_GCLOUD_STATE="${TEST_DIR}/state"
mkdir -p -- "${FAKE_GCLOUD_STATE}"

project="example-project"
credential="${TEST_DIR}/project/secrets/GOOGLE_APPLICATION_CREDENTIALS"
previous_credential="${credential}.previous"
helper="${TEST_DIR}/project/scripts/configure-dev-cloud-ocr.sh"
validator="${TEST_DIR}/project/ci/validate-dev-cloud-ocr-credential.sh"

if CI=true GCLOUD_PROJECT="${project}" "${helper}" configure \
  >"${TEST_DIR}/ci.out" 2>"${TEST_DIR}/ci.err"; then
  echo "configure unexpectedly accepted a CI environment" >&2
  exit 1
fi
[ "$(<"${TEST_DIR}/ci.err")" = \
  "dev Cloud OCR credential setup failed: this interactive developer helper cannot run in CI" ]
[ ! -e "${credential}" ]
[ ! -e "${FAKE_GCLOUD_STATE}/login-count" ]

# GitHub Actions exports CI=true for the contract harness. The helper is
# intentionally interactive, so exercise its remaining lifecycle outside CI
# after proving that the production guard fails closed.
unset CI

GCLOUD_PROJECT="${project}" "${helper}" configure >/dev/null
"${validator}" "${credential}" "${project}"
[ "$(stat -c '%a' "${credential}")" = "600" ]
[ "$(jq -r '.source_credentials.refresh_token' "${credential}")" = "refresh-1" ]

if "${validator}" "${credential}" "different-project" >"${TEST_DIR}/project.out" 2>"${TEST_DIR}/project.err"; then
  echo "validator accepted an ADC for a different GCP project" >&2
  exit 1
fi
if "${validator}" "${credential}" "" >"${TEST_DIR}/missing-project.out" 2>"${TEST_DIR}/missing-project.err"; then
  echo "validator accepted an ADC with an empty expected GCP project" >&2
  exit 1
fi
[ "$(<"${TEST_DIR}/missing-project.err")" = \
  "Dev Cloud OCR credential is invalid: the expected Google Cloud project ID is missing or invalid" ]

if GCLOUD_PROJECT="${project}" "${helper}" configure >"${TEST_DIR}/configure.out" 2>"${TEST_DIR}/configure.err"; then
  echo "configure unexpectedly overwrote an existing ADC" >&2
  exit 1
fi

touch "${FAKE_GCLOUD_STATE}/fail-next-revoke"
if GCLOUD_PROJECT="${project}" "${helper}" rotate >"${TEST_DIR}/failed-rotate.out" 2>"${TEST_DIR}/failed-rotate.err"; then
  echo "rotate unexpectedly succeeded when prior-credential revocation failed" >&2
  exit 1
fi
"${validator}" "${credential}" "${project}"
[ "$(jq -r '.source_credentials.refresh_token' "${credential}")" = "refresh-2" ]
"${validator}" "${previous_credential}" "${project}"
[ "$(stat -c '%a' "${previous_credential}")" = "600" ]
[ "$(jq -r '.source_credentials.refresh_token' "${previous_credential}")" = "refresh-1" ]
[ "$(<"${FAKE_GCLOUD_STATE}/revoke-attempt.log")" = "refresh-1" ]
[ ! -e "${FAKE_GCLOUD_STATE}/revoke.log" ]
rg -qF "${previous_credential}" "${TEST_DIR}/failed-rotate.err"

GCLOUD_PROJECT="${project}" "${helper}" rotate >/dev/null
[ "$(jq -r '.source_credentials.refresh_token' "${credential}")" = "refresh-2" ]
[ ! -e "${previous_credential}" ]
[ "$(<"${FAKE_GCLOUD_STATE}/login-count")" -eq 2 ]
[ "$(<"${FAKE_GCLOUD_STATE}/revoke.log")" = "refresh-1" ]

GCLOUD_PROJECT="${project}" "${helper}" rotate >/dev/null
"${validator}" "${credential}" "${project}"
[ "$(jq -r '.source_credentials.refresh_token' "${credential}")" = "refresh-3" ]
[ ! -e "${previous_credential}" ]
[ "$(<"${FAKE_GCLOUD_STATE}/revoke.log")" = $'refresh-1\nrefresh-2' ]

touch "${FAKE_GCLOUD_STATE}/fail-next-revoke"
if GCLOUD_PROJECT="${project}" "${helper}" rotate >"${TEST_DIR}/failed-rotate-revoke.out" 2>"${TEST_DIR}/failed-rotate-revoke.err"; then
  echo "second rotate unexpectedly succeeded when prior-credential revocation failed" >&2
  exit 1
fi
[ "$(jq -r '.source_credentials.refresh_token' "${credential}")" = "refresh-4" ]
[ "$(jq -r '.source_credentials.refresh_token' "${previous_credential}")" = "refresh-3" ]

GCLOUD_PROJECT="${project}" "${helper}" revoke >/dev/null
[ ! -e "${credential}" ]
[ ! -e "${previous_credential}" ]
[ "$(<"${FAKE_GCLOUD_STATE}/revoke.log")" = $'refresh-1\nrefresh-2\nrefresh-3\nrefresh-4' ]

mkdir -p -- "$(dirname "${credential}")"
printf '%s\n' '{"type":"service_account","private_key":"forbidden"}' >"${credential}"
if "${validator}" "${credential}" "${project}" >"${TEST_DIR}/key.out" 2>"${TEST_DIR}/key.err"; then
  echo "validator accepted a long-lived service-account key" >&2
  exit 1
fi

jq -n '{
  type: "impersonated_service_account",
  service_account_impersonation_url: "https://attacker.example/generateAccessToken",
  source_credentials: {
    type: "authorized_user",
    client_id: "client-id",
    client_secret: "client-secret",
    refresh_token: "refresh-token"
  }
}' >"${credential}"
if "${validator}" "${credential}" "${project}" >"${TEST_DIR}/url.out" 2>"${TEST_DIR}/url.err"; then
  echo "validator accepted an untrusted impersonation endpoint" >&2
  exit 1
fi

if rg -n 'service-accounts keys (create|delete)|private_key' \
  "${ROOT_DIR}/scripts/configure-dev-cloud-ocr.sh"; then
  echo "dev Cloud OCR helper must not create, delete, or handle private keys" >&2
  exit 1
fi

echo "Dev Cloud OCR keyless credential lifecycle passed."
