#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MARIADB_IMAGE="${MARIADB_IMAGE:-mariadb:12.3@sha256:b1c7bf836e64ed9406a8984af29509f40089d55cea14b32f12c4726a1f17104b}"
GO_TEST_IMAGE="${GO_TEST_IMAGE:-golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2}"
ALPINE_IMAGE="${ALPINE_IMAGE:-alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b}"
DB_NAME=scribe
DB_USER=scribe
DB_PASSWORD=restore-smoke-password
DB_ROOT_PASSWORD=restore-smoke-root-password
RUN_SUFFIX="$(date +%s)-$$"
RESOURCE_PREFIX="scribe-restore-${RUN_SUFFIX}"
NETWORK_NAME="${RESOURCE_PREFIX}-network"
SOURCE_DB="${RESOURCE_PREFIX}-source-db"
RESTORE_DB="${RESOURCE_PREFIX}-restore-db"
SOURCE_BLOBS="${RESOURCE_PREFIX}-source-blobs"
RESTORE_BLOBS="${RESOURCE_PREFIX}-restore-blobs"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/scribe-restore.XXXXXX")"
HELPER_CONTAINERS=()

cleanup() {
  for container in "${HELPER_CONTAINERS[@]}"; do
    docker rm -f "${container}" >/dev/null 2>&1 || true
  done
  docker rm -f "${SOURCE_DB}" "${RESTORE_DB}" >/dev/null 2>&1 || true
  docker volume rm "${SOURCE_BLOBS}" "${RESTORE_BLOBS}" >/dev/null 2>&1 || true
  docker network rm "${NETWORK_NAME}" >/dev/null 2>&1 || true
  rm -rf "${TEMP_DIR}"
}
trap cleanup EXIT

if ! command -v docker >/dev/null 2>&1; then
  echo "Error: docker is required to exercise backup and restore." >&2
  exit 127
fi

wait_for_database() {
  local container="$1"
  for _ in $(seq 1 90); do
    if docker exec "${container}" mariadb \
      --batch \
      --skip-column-names \
      --protocol=tcp \
      --host=127.0.0.1 \
      --user=root \
      --password="${DB_ROOT_PASSWORD}" \
      --execute='SELECT 1' >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "Database ${container} did not become ready." >&2
  return 1
}

copy_repo_to_container() {
  local container="$1"

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
    -C "${ROOT_DIR}" -cf - . | docker cp - "${container}:/app"
}

start_database() {
  local container="$1"
  docker run --detach \
    --name "${container}" \
    --network "${NETWORK_NAME}" \
    --env "MARIADB_DATABASE=${DB_NAME}" \
    --env "MARIADB_USER=${DB_USER}" \
    --env "MARIADB_PASSWORD=${DB_PASSWORD}" \
    --env "MARIADB_ROOT_PASSWORD=${DB_ROOT_PASSWORD}" \
    "${MARIADB_IMAGE}" >/dev/null
  wait_for_database "${container}"
}

docker network create "${NETWORK_NAME}" >/dev/null
docker volume create "${SOURCE_BLOBS}" >/dev/null
docker volume create "${RESTORE_BLOBS}" >/dev/null
start_database "${SOURCE_DB}"

GO_TEST_CONTAINER="$(docker create \
  --network "${NETWORK_NAME}" \
  --workdir /app \
  "${GO_TEST_IMAGE}" \
  sh -lc 'sleep 1800')"
HELPER_CONTAINERS+=("${GO_TEST_CONTAINER}")
copy_repo_to_container "${GO_TEST_CONTAINER}"
docker start "${GO_TEST_CONTAINER}" >/dev/null

docker exec \
  --env "TEST_DSN=${DB_USER}:${DB_PASSWORD}@tcp(${SOURCE_DB}:3306)/${DB_NAME}?parseTime=true" \
  --env SCRIBE_RESTORE_SMOKE=1 \
  --env SCRIBE_RESTORE_PHASE=source \
  "${GO_TEST_CONTAINER}" \
  sh -lc 'CGO_ENABLED=0 timeout 600 /usr/local/go/bin/go test -v ./internal/database -run "^TestBackupRestoreMigrationLedger$" -count=1'

docker exec -i "${SOURCE_DB}" mariadb \
  --protocol=tcp \
  --host=127.0.0.1 \
  --user=root \
  --password="${DB_ROOT_PASSWORD}" \
  "${DB_NAME}" <<'SQL'
INSERT INTO items (id, user_id, workspace_id, name, source_type, source_url)
VALUES ('restore-smoke-item', 1, 1, 'Restore smoke item', 'manifest', 'https://source.example/manifest');
INSERT INTO item_images (id, workspace_id, item_id, sequence, image_url, canvas_uri, width, height, label)
VALUES (99001, 1, 'restore-smoke-item', 1, '/static/uploads/restore-smoke.bin', 'https://source.example/canvas/restore-smoke', 1200, 800, 'Restore smoke canvas');
INSERT INTO contexts (id, name, segmentation_model, transcription_provider, transcription_model)
VALUES (99001, 'Restore smoke context', 'tesseract', 'tesseract', 'tesseract');
INSERT INTO annotation_pages (workspace_id, item_image_id, page_id, canvas_uri, payload, revision)
VALUES (
  1,
  99001,
  'https://scribe.example/item-image-99001/canvas/page-1/annotations',
  'https://source.example/canvas/restore-smoke',
  '{"@context":["http://iiif.io/api/extension/text-granularity/context.json","http://iiif.io/api/presentation/3/context.json"],"id":"https://scribe.example/item-image-99001/canvas/page-1/annotations","type":"AnnotationPage","items":[{"id":"https://scribe.example/item-image-99001/canvas/page-1/annotations/items/0123456789abcdef0123456789abcdef","type":"Annotation","motivation":"supplementing","textGranularity":"line","body":{"type":"TextualBody","purpose":"supplementing","format":"text/plain","value":"Restored correction"},"target":"https://source.example/canvas/restore-smoke#xywh=10,20,300,40"}]}',
  7
);
INSERT INTO annotations (workspace_id, item_image_id, id, canvas_uri, text_granularity, position, payload)
VALUES (
  1,
  99001,
  'https://scribe.example/item-image-99001/canvas/page-1/annotations/items/0123456789abcdef0123456789abcdef',
  'https://source.example/canvas/restore-smoke',
  'line',
  0,
  '{"id":"https://scribe.example/item-image-99001/canvas/page-1/annotations/items/0123456789abcdef0123456789abcdef","type":"Annotation","motivation":"supplementing","textGranularity":"line","body":{"type":"TextualBody","purpose":"supplementing","format":"text/plain","value":"Restored correction"},"target":"https://source.example/canvas/restore-smoke#xywh=10,20,300,40"}'
);
INSERT INTO published_annotation_pages (
  workspace_id, item_image_id, page_id, canvas_uri, payload,
  published_revision, published_by_user_id, published_at
)
SELECT
  workspace_id, item_image_id, page_id, canvas_uri, payload,
  revision, NULL, CURRENT_TIMESTAMP(6)
FROM annotation_pages
WHERE item_image_id = 99001;
INSERT INTO transcription_jobs (
  id, workspace_id, item_image_id, context_id, context_snapshot, input_revision, status,
  attempt_count, max_attempts, lease_until, locked_by
) SELECT
  99001,
  i.workspace_id,
  ii.id,
  99001,
  '{"id":99001,"name":"Restore smoke context","is_default":false,"segmentation_model":"tesseract","transcription_provider":"tesseract","transcription_model":"tesseract","created_at":"2026-07-20T00:00:00Z","updated_at":"2026-07-20T00:00:00Z"}',
  7,
  'running',
  3,
  3,
  DATE_SUB(NOW(), INTERVAL 1 MINUTE),
  'expired-restore-worker'
FROM item_images ii
JOIN items i ON i.id = ii.item_id
WHERE ii.id = 99001;
INSERT INTO transcription_job_attempts (
  job_id, attempt_number, context_snapshot, input_revision,
  lease_owner, lease_token, outcome, started_at
)
SELECT
  id, attempt_count, context_snapshot, input_revision,
  'scribe-worker@restore-smoke', locked_by, 'running',
  DATE_SUB(NOW(6), INTERVAL 2 MINUTE)
FROM transcription_jobs
WHERE id = 99001;
INSERT INTO annotation_mirror_outbox (
  item_image_id, revision, payload, status, attempt_count, max_attempts,
  lease_until, locked_by
)
SELECT
  item_image_id, published_revision, payload, 'processing', 20, 20,
  DATE_SUB(NOW(), INTERVAL 1 MINUTE), 'expired-restore-mirror'
FROM published_annotation_pages WHERE item_image_id = 99001;
SQL

docker run --rm \
  --volume "${SOURCE_BLOBS}:/data" \
  "${ALPINE_IMAGE}" \
  sh -c 'mkdir -p /data/uploads && printf %s restored-source-blob > /data/uploads/restore-smoke.bin'
SOURCE_BLOB_HASH="$(docker run --rm --volume "${SOURCE_BLOBS}:/data:ro" "${ALPINE_IMAGE}" sha256sum /data/uploads/restore-smoke.bin | awk '{print $1}')"

docker exec "${SOURCE_DB}" mariadb-dump \
  --protocol=tcp \
  --host=127.0.0.1 \
  --user=root \
  --password="${DB_ROOT_PASSWORD}" \
  --single-transaction \
  --routines \
  --triggers \
  "${DB_NAME}" > "${TEMP_DIR}/database.sql"
if ! grep -F 'scribe_schema_migrations' "${TEMP_DIR}/database.sql" >/dev/null; then
  echo "Database backup omitted the migration ledger." >&2
  exit 1
fi

BLOB_BACKUP_CONTAINER="$(docker create --volume "${SOURCE_BLOBS}:/source:ro" "${ALPINE_IMAGE}" sh -c 'tar -czf /tmp/uploads.tgz -C /source .')"
HELPER_CONTAINERS+=("${BLOB_BACKUP_CONTAINER}")
docker start -a "${BLOB_BACKUP_CONTAINER}" >/dev/null
docker cp "${BLOB_BACKUP_CONTAINER}:/tmp/uploads.tgz" "${TEMP_DIR}/uploads.tgz"

start_database "${RESTORE_DB}"
docker exec -i "${RESTORE_DB}" mariadb \
  --protocol=tcp \
  --host=127.0.0.1 \
  --user=root \
  --password="${DB_ROOT_PASSWORD}" \
  "${DB_NAME}" < "${TEMP_DIR}/database.sql"

BLOB_RESTORE_CONTAINER="$(docker create --volume "${RESTORE_BLOBS}:/restore" "${ALPINE_IMAGE}" sh -c 'tar -xzf /tmp/uploads.tgz -C /restore')"
HELPER_CONTAINERS+=("${BLOB_RESTORE_CONTAINER}")
docker cp "${TEMP_DIR}/uploads.tgz" "${BLOB_RESTORE_CONTAINER}:/tmp/uploads.tgz"
docker start -a "${BLOB_RESTORE_CONTAINER}" >/dev/null
RESTORED_BLOB_HASH="$(docker run --rm --volume "${RESTORE_BLOBS}:/data:ro" "${ALPINE_IMAGE}" sha256sum /data/uploads/restore-smoke.bin | awk '{print $1}')"
if [ "${RESTORED_BLOB_HASH}" != "${SOURCE_BLOB_HASH}" ]; then
  echo "Restored upload blob hash does not match the source." >&2
  exit 1
fi

docker exec \
  --env "TEST_DSN=${DB_USER}:${DB_PASSWORD}@tcp(${RESTORE_DB}:3306)/${DB_NAME}?parseTime=true" \
  --env SCRIBE_RESTORE_SMOKE=1 \
  --env SCRIBE_RESTORE_PHASE=restore \
  "${GO_TEST_CONTAINER}" \
  sh -lc 'CGO_ENABLED=0 timeout 600 /usr/local/go/bin/go test -p=1 -v ./internal/database ./internal/store -run "^TestBackupRestore(MigrationLedger|IntegrityAndJobRecovery)$" -count=1'

echo "Backup/restore smoke passed: the clean migration ledger, canonical and published IIIF, derived index, expired-job recovery, and upload blob integrity are intact."
