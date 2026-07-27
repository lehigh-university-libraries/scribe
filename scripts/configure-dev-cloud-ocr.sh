#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CREDENTIAL_FILE="${ROOT_DIR}/secrets/GOOGLE_APPLICATION_CREDENTIALS"
PREVIOUS_CREDENTIAL_FILE="${CREDENTIAL_FILE}.previous"
ACTION="${1:-configure}"

fail() {
  echo "dev Cloud OCR credential setup failed: $*" >&2
  exit 1
}

usage() {
  cat >&2 <<'EOF'
usage: GCLOUD_PROJECT=project-id scripts/configure-dev-cloud-ocr.sh [configure|rotate|revoke]

Creates or revokes the repository-local, keyless ADC file used by
`make up-cloud-ocr`. No service-account private key is created or downloaded.
EOF
  exit 2
}

case "${ACTION}" in
  configure | rotate | revoke) ;;
  *) usage ;;
esac

if [ "${CI:-}" = "true" ]; then
  fail "this interactive developer helper cannot run in CI"
fi

command -v gcloud >/dev/null 2>&1 || fail "gcloud is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"

: "${GCLOUD_PROJECT:?GCLOUD_PROJECT is required}"
[[ "${GCLOUD_PROJECT}" =~ ^[a-z][a-z0-9-]{4,28}[a-z0-9]$ ]] ||
  fail "GCLOUD_PROJECT is not a canonical Google Cloud project ID"

TARGET_SERVICE_ACCOUNT="scribe-dev-external@${GCLOUD_PROJECT}.iam.gserviceaccount.com"
validate_credential() {
  local path="$1"
  "${ROOT_DIR}/ci/validate-dev-cloud-ocr-credential.sh" \
    "${path}" "${GCLOUD_PROJECT}" >/dev/null
}

TEMPORARY_ROOT="$(mktemp -d -- "${TMPDIR:-/tmp}/scribe-cloud-ocr.XXXXXXXXXX")"
cleanup() {
  rm -rf -- "${TEMPORARY_ROOT}"
}
trap cleanup EXIT
umask 077

revoke_file() {
  local source="$1"
  local revoke_config="${TEMPORARY_ROOT}/revoke"
  mkdir -p -- "${revoke_config}"
  install -m 0600 -- "${source}" "${revoke_config}/application_default_credentials.json"
  CLOUDSDK_CONFIG="${revoke_config}" \
    gcloud auth application-default revoke --quiet >/dev/null
}

revoke_previous_file() {
  [ -e "${PREVIOUS_CREDENTIAL_FILE}" ] || return 0
  [ -f "${PREVIOUS_CREDENTIAL_FILE}" ] ||
    fail "the retained prior ADC is not a regular file: ${PREVIOUS_CREDENTIAL_FILE}"
  validate_credential "${PREVIOUS_CREDENTIAL_FILE}" ||
    fail "refusing to revoke an invalid retained prior ADC at ${PREVIOUS_CREDENTIAL_FILE}"
  revoke_file "${PREVIOUS_CREDENTIAL_FILE}" ||
    fail "the prior ADC could not be revoked and remains at ${PREVIOUS_CREDENTIAL_FILE} for a retry"
  rm -f -- "${PREVIOUS_CREDENTIAL_FILE}"
}

if [ "${ACTION}" = "revoke" ]; then
  [ -f "${CREDENTIAL_FILE}" ] || fail "${CREDENTIAL_FILE} does not exist"
  validate_credential "${CREDENTIAL_FILE}" ||
    fail "refusing to revoke a credential that is not the exact dev impersonation ADC"
  revoke_previous_file
  revoke_file "${CREDENTIAL_FILE}" ||
    fail "gcloud could not revoke the ADC; the local file was retained for a retry"
  rm -f -- "${CREDENTIAL_FILE}"
  echo "Revoked and removed ${CREDENTIAL_FILE}."
  exit 0
fi

if [ "${ACTION}" = "configure" ] && [ -e "${CREDENTIAL_FILE}" ]; then
  fail "${CREDENTIAL_FILE} already exists; use the rotate action or revoke it first"
fi
if [ "${ACTION}" = "rotate" ]; then
  [ -f "${CREDENTIAL_FILE}" ] || fail "nothing is configured; use the configure action"
  validate_credential "${CREDENTIAL_FILE}" ||
    fail "refusing to rotate a credential that is not the exact dev impersonation ADC"
  if [ -e "${PREVIOUS_CREDENTIAL_FILE}" ]; then
    revoke_previous_file
    echo "Completed the pending prior-ADC revocation; the current ADC was retained."
    exit 0
  fi
fi

LOGIN_CONFIG="${TEMPORARY_ROOT}/login"
mkdir -p -- "${LOGIN_CONFIG}"
CLOUDSDK_CONFIG="${LOGIN_CONFIG}" \
  gcloud auth application-default login \
    --impersonate-service-account="${TARGET_SERVICE_ACCOUNT}" \
    --disable-quota-project \
    --quiet

GENERATED_FILE="${LOGIN_CONFIG}/application_default_credentials.json"
[ -f "${GENERATED_FILE}" ] || fail "gcloud did not create an ADC file"
validate_credential "${GENERATED_FILE}" ||
  fail "gcloud produced an unsupported or unexpectedly scoped credential"

mkdir -p -- "${ROOT_DIR}/secrets"
chmod 0700 -- "${ROOT_DIR}/secrets"
if [ "${ACTION}" = "rotate" ]; then
  install -m 0600 -- "${CREDENTIAL_FILE}" "${PREVIOUS_CREDENTIAL_FILE}"
fi
install -m 0600 -- "${GENERATED_FILE}" "${CREDENTIAL_FILE}"

if [ "${ACTION}" = "rotate" ]; then
  if ! revoke_file "${PREVIOUS_CREDENTIAL_FILE}"; then
    fail "the new ADC is installed, but the prior ADC could not be revoked; it remains at ${PREVIOUS_CREDENTIAL_FILE}; rerun rotate or revoke to retry"
  fi
  rm -f -- "${PREVIOUS_CREDENTIAL_FILE}"
fi

echo "Installed keyless dev Cloud OCR credentials at ${CREDENTIAL_FILE}."
