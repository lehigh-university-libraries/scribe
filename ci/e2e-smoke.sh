#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_TEST_IMAGE="${GO_TEST_IMAGE:-golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2}"

cd "${ROOT_DIR}"

if ! command -v docker >/dev/null 2>&1; then
  echo "Error: docker is required to run the e2e smoke test." >&2
  exit 127
fi

test -f .env || cp sample.env .env
bash generate-secrets.sh
docker compose up -d --wait --wait-timeout 120 mariadb

MARIADB_ID="$(docker compose ps -q mariadb | head -1)"
if [ -z "${MARIADB_ID}" ]; then
  echo "Error: mariadb container is not running." >&2
  exit 1
fi

NETWORK="$(docker inspect "${MARIADB_ID}" --format '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' | awk '{print $1}')"
if [ -z "${NETWORK}" ]; then
  echo "Error: could not detect the docker compose network." >&2
  exit 1
fi

DB_USER="${MARIADB_USER:-scribe}"
DB_NAME="${MARIADB_DATABASE:-scribe}"
if [ -f "./secrets/mariadb_password" ]; then
  DB_PASSWORD="$(tr -d '\n' < ./secrets/mariadb_password)"
else
  DB_PASSWORD="scribe"
fi

TEST_DSN_VALUE="${DB_USER}:${DB_PASSWORD}@tcp(mariadb:3306)/${DB_NAME}?parseTime=true"
# This script is evaluated inside the Go test container, where PATH must expand.
# shellcheck disable=SC2016
RUN_TESTS='
  export PATH="/usr/local/go/bin:$PATH"
  apk add --no-cache build-base >/dev/null
  # Both packages use the same acceptance schema. Keep their package binaries
  # sequential while retaining concurrency inside each package, and bypass the
  # test-result entries held in the persistent build cache.
  go test -p=1 -count=1 -v ./internal/server ./internal/store \
    -run "TestManifestIngestLoadsHOCRAnnotations|TestAnnotationPageRevisionSaveSemantics|TestEnrichAnnotationPageIsAtomicAndPreservesPageProperties|TestExternalCanvasResolutionDuringEnrichmentIsTenantScoped|TestLocalAnnotationCRUDSavedAsWholePagesPreservesCanonicalProperties|TestAnnotationReconciliationPersistsThroughRealConnectSaveReload|TestAnnotationPagesAreWorkspaceIsolatedAndRevisioned|TestTranscriptionCommitAtomicallyFencesAndCompletesJob|TestTranscriptionJobRequiresCanonicalInputRevision"
'

container_id="$(docker create \
  --network "${NETWORK}" \
  --mount "type=volume,src=scribe-go-test-build-cache-v1,dst=/root/.cache/go-build" \
  --mount "type=volume,src=scribe-go-test-mod-cache-v1,dst=/go/pkg/mod" \
  -e "TEST_DSN=${TEST_DSN_VALUE}" \
  -w /app \
  "${GO_TEST_IMAGE}" \
  sh -lc "${RUN_TESTS}")"
cleanup() {
  docker rm -f "${container_id}" >/dev/null 2>&1 || true
}
trap cleanup EXIT
tar \
  --exclude=.env \
  --exclude=.git \
  --exclude=.tools \
  --exclude='gha-creds-*.json' \
  --exclude=secrets \
  --exclude=terraform/.terraform \
  --exclude=site \
  --exclude='web/node_modules*' \
  --exclude=web/dist \
  --exclude='mirador-scribe/node_modules*' \
  --exclude=mirador-scribe/dist \
  -C "${ROOT_DIR}" -cf - . | docker cp - "${container_id}:/app"
docker start -a "${container_id}"
