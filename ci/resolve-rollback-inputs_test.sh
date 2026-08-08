#!/usr/bin/env bash

set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
resolver="$root_dir/ci/resolve-rollback-inputs.sh"
fixture="$root_dir/ci/fixtures/deployment-inputs.json"
test_dir="$(mktemp -d)"
trap 'rm -rf "$test_dir"' EXIT

normalized="$($resolver "$fixture")"
jq -eS . <<<"$normalized" >/dev/null
[ "$(jq -r '.docker_compose_sha' <<<"$normalized")" = "0123456789abcdef0123456789abcdef01234567" ]
[ "$(jq -r '.data_generation' <<<"$normalized")" = "canonical-v1" ]
jq -e '.ocr_service_images | has("ollama/glm-ocr:bf16")' <<<"$normalized" >/dev/null

legacy_fixture="$test_dir/deployment-inputs-before-dev-ocr-iam.json"
jq 'del(.configuration.dev_external_ocr_impersonators)' "$fixture" >"$legacy_fixture"
legacy_normalized="$($resolver "$legacy_fixture")"
jq -e '.configuration.dev_external_ocr_impersonators == []' <<<"$legacy_normalized" >/dev/null

legacy_browser_fixture="$test_dir/deployment-inputs-before-browser-readiness.json"
jq 'del(.browser_readiness_image)' "$fixture" >"$legacy_browser_fixture"
legacy_browser_normalized="$($resolver "$legacy_browser_fixture")"
jq -e '.browser_readiness_image == ""' <<<"$legacy_browser_normalized" >/dev/null

legacy_browser_subnet_fixture="$test_dir/deployment-inputs-before-browser-subnet.json"
jq 'del(.configuration.browser_readiness_subnet_cidr)' "$fixture" >"$legacy_browser_subnet_fixture"
legacy_browser_subnet_normalized="$($resolver "$legacy_browser_subnet_fixture")"
jq -e '.configuration.browser_readiness_subnet_cidr == "10.43.0.0/26"' \
  <<<"$legacy_browser_subnet_normalized" >/dev/null

legacy_machine_fixture="$test_dir/deployment-inputs-before-preview-machine-type.json"
jq 'del(.configuration.preview_machine_type)' "$fixture" >"$legacy_machine_fixture"
legacy_machine_normalized="$($resolver "$legacy_machine_fixture")"
jq -e '.configuration.preview_machine_type == "e2-medium"' \
  <<<"$legacy_machine_normalized" >/dev/null

assert_rejected() {
  local name="$1"
  local filter="$2"
  local candidate="$test_dir/${name}.json"

  jq "$filter" "$fixture" >"$candidate"
  if "$resolver" "$candidate" >/dev/null 2>&1; then
    echo "resolver accepted invalid fixture: $name" >&2
    exit 1
  fi
}

assert_rejected_with_error() {
  local name="$1"
  local filter="$2"
  local expected_error="$3"
  local candidate="$test_dir/${name}.json"
  local stderr_file="$test_dir/${name}.err"

  jq "$filter" "$fixture" >"$candidate"
  if "$resolver" "$candidate" >"$test_dir/${name}.out" 2>"$stderr_file"; then
    echo "resolver accepted invalid fixture: $name" >&2
    exit 1
  fi
  [ ! -s "$test_dir/${name}.out" ]
  [ "$(<"$stderr_file")" = "$expected_error" ] || {
    echo "resolver emitted the wrong bounded error for: $name" >&2
    exit 1
  }
  [ "$(wc -c <"$stderr_file")" -le 160 ] || {
    echo "resolver error was not bounded for: $name" >&2
    exit 1
  }
}

assert_rejected missing-configuration 'del(.configuration)'
assert_rejected extra-input '.unexpected = true'
assert_rejected zero-compose-sha '.docker_compose_sha = "0000000000000000000000000000000000000000"'
assert_rejected unsupported-generation '.data_generation = "canonical-v999"'
assert_rejected mutable-image '.api_image = "ghcr.io/lehigh-university-libraries/scribe:main"'
production_browser_fixture="$test_dir/production-browser-image.json"
jq '.browser_readiness_image = "us-docker.pkg.dev/scribe-test1/internal/scribe-browser-readiness@sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"' \
  "$fixture" >"$production_browser_fixture"
production_browser_normalized="$($resolver "$production_browser_fixture")"
jq -e '.browser_readiness_image == "us-docker.pkg.dev/scribe-test1/internal/scribe-browser-readiness@sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"' \
  <<<"$production_browser_normalized" >/dev/null
assert_rejected null-browser-image '.browser_readiness_image = null'
assert_rejected zero-browser-image '.browser_readiness_image = "us-docker.pkg.dev/scribe-test1/internal/scribe-browser-readiness@sha256:0000000000000000000000000000000000000000000000000000000000000000"'
assert_rejected cross-project-browser-image '.browser_readiness_image = "us-docker.pkg.dev/other-project/internal/scribe-browser-readiness@sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"'
assert_rejected mutable-browser-image '.browser_readiness_image = "us-docker.pkg.dev/scribe-test1/internal/scribe-browser-readiness:main"'
assert_rejected null-browser-subnet '.configuration.browser_readiness_subnet_cidr = null'
assert_rejected broad-browser-subnet '.configuration.browser_readiness_subnet_cidr = "10.43.0.0/24"'
assert_rejected null-preview-machine-type '.configuration.preview_machine_type = null'
assert_rejected unreviewed-preview-machine-type '.configuration.preview_machine_type = "n2d-standard-96"'
assert_rejected unsafe-ocr-service-key '.ocr_service_images["ollama/glm-ocr:bf16\nINJECTED"] = .ocr_service_images["ollama/glm-ocr:bf16"]'
assert_rejected unused-frontend-source-image '.frontend_image = "ghcr.io/lehigh-university-libraries/scribe-frontend@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"'
assert_rejected cross-project-image '.frontend_gar_image = "us-docker.pkg.dev/other-project/internal/scribe-frontend@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"'
assert_rejected unsafe-cidr '.configuration.allowed_ips = ["192.0.2.0/24\nINJECTED=true"]'
assert_rejected invalid-zone '.configuration.zone = "us-west1-b"'
assert_rejected missing-monitoring '.configuration.monitoring_notification_channels = []'
assert_rejected fractional-limit '.configuration.storage_max_items_total = 50000.5'
assert_rejected inverted-limit '.configuration.storage_max_bytes_total = 1'
assert_rejected unsafe-email '.configuration.vault_admin_emails = ["admin@example.com"]'
assert_rejected invalid-backup-identity '.configuration.backup_restore_service_account_email = "backup@example.com"'
dev_ocr_error="Deployment inputs rejected: dev_external_ocr_impersonators must be empty because external OCR impersonation is restricted to the dev workspace."
assert_rejected_with_error \
  prod-external-ocr-impersonator \
  '.configuration.dev_external_ocr_impersonators = ["user:developer@example.edu"]' \
  "$dev_ocr_error"
assert_rejected_with_error \
  invalid-external-ocr-impersonators \
  '.configuration.dev_external_ocr_impersonators = "user:developer@example.edu"' \
  "$dev_ocr_error"
assert_rejected_with_error \
  null-external-ocr-impersonators \
  '.configuration.dev_external_ocr_impersonators = null' \
  "$dev_ocr_error"
assert_rejected_with_error \
  bounded-external-ocr-error \
  '.configuration.dev_external_ocr_impersonators = [("user:" + ("x" * 4096))]' \
  "$dev_ocr_error"

echo "rollback input resolver contract passed"
