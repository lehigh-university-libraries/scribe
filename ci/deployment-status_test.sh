#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

assert_status() {
  local expected="$1"
  shift
  local actual
  actual="$(env -i PATH="$PATH" HOME="${HOME:-/tmp}" "$@" "$ROOT_DIR/ci/deployment-status.sh")"
  [ "$actual" = "$expected" ] || {
    echo "expected status $expected, got $actual for: $*" >&2
    exit 1
  }
}

assert_status failure DEPLOY_MODE=apply PLAN_OUTCOME=failure APPLY_OUTCOME=skipped READINESS_OUTCOME=skipped
assert_status failure DEPLOY_MODE=apply PLAN_OUTCOME=success APPLY_OUTCOME=failure READINESS_OUTCOME=skipped ROLLBACK_OUTCOME=skipped
assert_status apply-failed-rolled-back DEPLOY_MODE=apply PLAN_OUTCOME=success APPLY_OUTCOME=failure READINESS_OUTCOME=skipped ROLLBACK_OUTCOME=success
assert_status rollback-failed DEPLOY_MODE=apply PLAN_OUTCOME=success APPLY_OUTCOME=failure READINESS_OUTCOME=skipped ROLLBACK_OUTCOME=failure
assert_status cancelled DEPLOY_MODE=apply PLAN_OUTCOME=success APPLY_OUTCOME=cancelled READINESS_OUTCOME=skipped
assert_status failure DEPLOY_MODE=apply PR_NUMBER=75 PLAN_PREVIEW_OUTCOME=success APPLY_PREVIEW_OUTCOME=failure READINESS_OUTCOME=skipped
assert_status attestation-failure DEPLOY_MODE=apply PLAN_OUTCOME=success APPLY_OUTCOME=success REVISION_OUTCOME=failure READINESS_OUTCOME=skipped ROLLBACK_OUTCOME=skipped
assert_status attestation-failed-rolled-back DEPLOY_MODE=apply PLAN_OUTCOME=success APPLY_OUTCOME=success REVISION_OUTCOME=failure READINESS_OUTCOME=skipped ROLLBACK_OUTCOME=success
assert_status rollback-failed DEPLOY_MODE=apply PLAN_OUTCOME=success APPLY_OUTCOME=success REVISION_OUTCOME=failure READINESS_OUTCOME=skipped ROLLBACK_OUTCOME=failure
assert_status failure DEPLOY_MODE=apply PLAN_OUTCOME=success APPLY_OUTCOME=success REVISION_OUTCOME=success READINESS_OUTCOME=failure ROLLBACK_OUTCOME=skipped
assert_status readiness-failed-rolled-back DEPLOY_MODE=apply PLAN_OUTCOME=success APPLY_OUTCOME=success REVISION_OUTCOME=success READINESS_OUTCOME=failure ROLLBACK_OUTCOME=success
assert_status rollback-failed DEPLOY_MODE=apply PLAN_OUTCOME=success APPLY_OUTCOME=success REVISION_OUTCOME=success READINESS_OUTCOME=failure ROLLBACK_OUTCOME=failure
assert_status url-failure DEPLOY_MODE=apply PLAN_OUTCOME=success APPLY_OUTCOME=success REVISION_OUTCOME=success READINESS_OUTCOME=success URL_OUTCOME=failure
assert_status backup-verification-failure DEPLOY_MODE=apply PLAN_OUTCOME=success APPLY_OUTCOME=success REVISION_OUTCOME=success URL_OUTCOME=success READINESS_OUTCOME=success BACKUP_OUTCOME=failure
assert_status success DEPLOY_MODE=apply PLAN_OUTCOME=success APPLY_OUTCOME=success REVISION_OUTCOME=success URL_OUTCOME=success READINESS_OUTCOME=success BACKUP_OUTCOME=success ROLLBACK_OUTCOME=skipped
assert_status success DEPLOY_MODE=apply PR_NUMBER=75 PLAN_PREVIEW_OUTCOME=success APPLY_PREVIEW_OUTCOME=success REVISION_OUTCOME=success URL_OUTCOME=success READINESS_OUTCOME=success
assert_status failure DEPLOY_MODE=plan PLAN_OUTCOME=failure
assert_status success DEPLOY_MODE=plan PR_NUMBER=75 PLAN_PREVIEW_OUTCOME=success
assert_status failure DEPLOY_MODE=destroy DESTROY_OUTCOME=failure
assert_status success DEPLOY_MODE=destroy PR_NUMBER=75 DESTROY_PREVIEW_OUTCOME=success

attestation_dir="$(mktemp -d)"
trap 'rm -rf "$attestation_dir"' EXIT
expected_image="us-docker.pkg.dev/example/internal/scribe-frontend@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
cat >"$attestation_dir/service.json" <<'JSON'
{
  "status": {
    "conditions": [{"type": "Ready", "status": "True"}],
    "latestCreatedRevisionName": "scribe-00042-abc",
    "latestReadyRevisionName": "scribe-00042-abc",
    "traffic": [{"revisionName": "scribe-00042-abc", "percent": 100}]
  }
}
JSON
cat >"$attestation_dir/revision.json" <<JSON
{
  "metadata": {"name": "scribe-00042-abc"},
  "spec": {"containers": [
    {"name": "proxy-power-button", "image": "example.invalid/ppb@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
    {"name": "frontend", "image": "$expected_image"}
  ]},
  "status": {"conditions": [{"type": "Ready", "status": "True"}]}
}
JSON
[ "$("$ROOT_DIR/ci/attest-cloud-run-revision.sh" "$attestation_dir/service.json" "$attestation_dir/revision.json" "$expected_image")" = "scribe-00042-abc" ]

jq '.status.traffic[0].percent = 99' "$attestation_dir/service.json" >"$attestation_dir/bad-traffic.json"
if "$ROOT_DIR/ci/attest-cloud-run-revision.sh" "$attestation_dir/bad-traffic.json" "$attestation_dir/revision.json" "$expected_image" >/dev/null 2>&1; then
  echo "attestation accepted less than 100% traffic" >&2
  exit 1
fi
jq '.spec.containers[1].image = "example.invalid/frontend:mutable"' "$attestation_dir/revision.json" >"$attestation_dir/bad-image.json"
if "$ROOT_DIR/ci/attest-cloud-run-revision.sh" "$attestation_dir/service.json" "$attestation_dir/bad-image.json" "$expected_image" >/dev/null 2>&1; then
  echo "attestation accepted the wrong frontend image" >&2
  exit 1
fi
jq '.status.latestReadyRevisionName = "scribe-00041-old"' "$attestation_dir/service.json" >"$attestation_dir/stale-ready.json"
if "$ROOT_DIR/ci/attest-cloud-run-revision.sh" "$attestation_dir/stale-ready.json" "$attestation_dir/revision.json" "$expected_image" >/dev/null 2>&1; then
  echo "attestation accepted a stale Ready revision" >&2
  exit 1
fi

echo "Deployment status matrix contracts passed."
