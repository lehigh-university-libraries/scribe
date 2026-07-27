#!/usr/bin/env bash

set -euo pipefail

fail() {
  echo "Dev Cloud OCR credential is invalid: $*" >&2
  exit 1
}

[ "$#" -eq 2 ] || fail "usage: $0 credential-file project-id"
CREDENTIAL_FILE="$1"
EXPECTED_PROJECT="$2"

command -v jq >/dev/null 2>&1 || fail "jq is required"
[[ "${EXPECTED_PROJECT}" =~ ^[a-z][a-z0-9-]{4,28}[a-z0-9]$ ]] ||
  fail "the expected Google Cloud project ID is missing or invalid"
[ -f "${CREDENTIAL_FILE}" ] || fail "${CREDENTIAL_FILE} is missing"

jq -e --arg expected_target "scribe-dev-external@${EXPECTED_PROJECT}.iam.gserviceaccount.com" '
  def exact_keys($allowed): ((keys - $allowed) | length) == 0;
  def target_url:
    test("^https://iamcredentials[.]googleapis[.]com/v1/projects/-/serviceAccounts/scribe-dev-external@[a-z][a-z0-9-]{4,28}[a-z0-9][.]iam[.]gserviceaccount[.]com:generateAccessToken$");
  type == "object" and
  exact_keys([
    "type", "service_account_impersonation_url", "source_credentials",
    "delegates", "scopes", "universe_domain"
  ]) and
  .type == "impersonated_service_account" and
  (.service_account_impersonation_url | target_url) and
  .service_account_impersonation_url ==
    ("https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/" + $expected_target + ":generateAccessToken") and
  ((.delegates // []) == []) and
  ((.scopes // []) == []) and
  ((.universe_domain // "googleapis.com") == "googleapis.com") and
  (.source_credentials | type == "object") and
  (.source_credentials | exact_keys([
    "type", "client_id", "client_secret", "refresh_token",
    "quota_project_id", "universe_domain", "account"
  ])) and
  .source_credentials.type == "authorized_user" and
  (.source_credentials.client_id | type == "string" and length > 0 and length <= 65536) and
  (.source_credentials.client_secret | type == "string" and length > 0 and length <= 65536) and
  (.source_credentials.refresh_token | type == "string" and length > 0 and length <= 65536) and
  ((.source_credentials.universe_domain // "googleapis.com") == "googleapis.com") and
  ([paths(objects) as $path | getpath($path) | has("private_key")] | any | not)
' "${CREDENTIAL_FILE}" >/dev/null 2>&1 ||
  fail "expected keyless, non-delegated scribe-dev-external impersonation ADC using only fixed Google endpoints"
