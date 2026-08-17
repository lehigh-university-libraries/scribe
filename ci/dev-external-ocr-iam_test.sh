#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "dev external OCR IAM test failed: $*" >&2
  exit 1
}

dev_replay="$(
  jq '.configuration.dev_external_ocr_impersonators = ["user:developer@example.edu", "group:scribe@example.edu"]' \
    ci/fixtures/deployment-inputs.json |
    GCLOUD_PROJECT=scribe-test1 ci/resolve-refresh-inputs.sh
)"
[ "$(jq -c '.configuration.dev_external_ocr_impersonators' <<<"$dev_replay")" = \
  '["user:developer@example.edu","group:scribe@example.edu"]' ] ||
  fail "refresh replay did not preserve explicit dev impersonators"
if jq '.configuration.dev_external_ocr_impersonators = ["serviceAccount:other@scribe-test1.iam.gserviceaccount.com"]' \
  ci/fixtures/deployment-inputs.json |
  GCLOUD_PROJECT=scribe-test1 ci/resolve-refresh-inputs.sh >/dev/null 2>&1; then
  fail "refresh replay accepted a non-human impersonator"
fi

echo "Dev external OCR replay validation passed."
