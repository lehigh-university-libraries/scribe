-- name: EnsureStorageQuotaUsage :exec
INSERT INTO storage_quota_usage (workspace_id)
VALUES (sqlc.arg(workspace_id))
ON DUPLICATE KEY UPDATE workspace_id = VALUES(workspace_id);

-- name: LockStorageQuotaUsage :one
SELECT
  workspace_id,
  upload_blob_bytes,
  database_bytes,
  item_count,
  image_count,
  reserved_upload_blob_bytes,
  reserved_database_bytes,
  reserved_item_count,
  reserved_image_count,
  updated_at
FROM storage_quota_usage
WHERE workspace_id = sqlc.arg(workspace_id)
FOR UPDATE;

-- Rebuild is rare maintenance. This ordered locking read also locks the
-- positive-key supremum gap, so a concurrently created workspace cannot add a
-- quota row between the tenant snapshot and the final global lock.
-- name: LockAllTenantStorageQuotaUsage :many
SELECT
  workspace_id,
  upload_blob_bytes,
  database_bytes,
  item_count,
  image_count,
  reserved_upload_blob_bytes,
  reserved_database_bytes,
  reserved_item_count,
  reserved_image_count,
  updated_at
FROM storage_quota_usage
WHERE workspace_id > 0
ORDER BY workspace_id
FOR UPDATE;

-- name: GetStorageQuotaUsage :one
SELECT
  workspace_id,
  upload_blob_bytes,
  database_bytes,
  item_count,
  image_count,
  reserved_upload_blob_bytes,
  reserved_database_bytes,
  reserved_item_count,
  reserved_image_count,
  updated_at
FROM storage_quota_usage
WHERE workspace_id = sqlc.arg(workspace_id);

-- name: AddStorageQuotaUsed :exec
UPDATE storage_quota_usage
SET
  upload_blob_bytes = upload_blob_bytes + sqlc.arg(upload_blob_bytes),
  database_bytes = database_bytes + sqlc.arg(database_bytes),
  item_count = item_count + sqlc.arg(item_count),
  image_count = image_count + sqlc.arg(image_count)
WHERE workspace_id IN (0, sqlc.arg(workspace_id));

-- name: SubtractStorageQuotaUsed :execresult
UPDATE storage_quota_usage
SET
  upload_blob_bytes = upload_blob_bytes - sqlc.arg(upload_blob_bytes),
  database_bytes = database_bytes - sqlc.arg(database_bytes),
  item_count = item_count - sqlc.arg(item_count),
  image_count = image_count - sqlc.arg(image_count)
WHERE workspace_id IN (0, sqlc.arg(workspace_id))
  AND upload_blob_bytes >= sqlc.arg(upload_blob_bytes)
  AND database_bytes >= sqlc.arg(database_bytes)
  AND item_count >= sqlc.arg(item_count)
  AND image_count >= sqlc.arg(image_count);

-- name: AddStorageQuotaReserved :exec
UPDATE storage_quota_usage
SET
  reserved_upload_blob_bytes = reserved_upload_blob_bytes + sqlc.arg(upload_blob_bytes),
  reserved_database_bytes = reserved_database_bytes + sqlc.arg(database_bytes),
  reserved_item_count = reserved_item_count + sqlc.arg(item_count),
  reserved_image_count = reserved_image_count + sqlc.arg(image_count)
WHERE workspace_id IN (0, sqlc.arg(workspace_id));

-- name: SubtractStorageQuotaReserved :execresult
UPDATE storage_quota_usage
SET
  reserved_upload_blob_bytes = reserved_upload_blob_bytes - sqlc.arg(upload_blob_bytes),
  reserved_database_bytes = reserved_database_bytes - sqlc.arg(database_bytes),
  reserved_item_count = reserved_item_count - sqlc.arg(item_count),
  reserved_image_count = reserved_image_count - sqlc.arg(image_count)
WHERE workspace_id IN (0, sqlc.arg(workspace_id))
  AND reserved_upload_blob_bytes >= sqlc.arg(upload_blob_bytes)
  AND reserved_database_bytes >= sqlc.arg(database_bytes)
  AND reserved_item_count >= sqlc.arg(item_count)
  AND reserved_image_count >= sqlc.arg(image_count);

-- name: InsertStorageQuotaReservation :exec
INSERT INTO workspace_storage_reservations (
  id,
  workspace_id,
  reserved_bytes,
  reserved_database_bytes,
  reserved_items,
  reserved_images,
  resource_key,
  expires_at
) VALUES (
  sqlc.arg(id),
  sqlc.arg(workspace_id),
  sqlc.arg(reserved_bytes),
  sqlc.arg(reserved_database_bytes),
  sqlc.arg(reserved_items),
  sqlc.arg(reserved_images),
  sqlc.narg(resource_key),
  sqlc.arg(expires_at)
);

-- name: LockStorageQuotaReservation :one
SELECT
  id,
  workspace_id,
  reserved_bytes,
  reserved_database_bytes,
  reserved_items,
  reserved_images,
  resource_key,
  expires_at,
  created_at
FROM workspace_storage_reservations
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id)
FOR UPDATE;

-- name: LockLiveStorageQuotaReservation :one
SELECT
  id,
  workspace_id,
  reserved_bytes,
  reserved_database_bytes,
  reserved_items,
  reserved_images,
  resource_key,
  expires_at,
  created_at
FROM workspace_storage_reservations
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id)
  AND expires_at > NOW(6)
FOR UPDATE;

-- name: DeleteStorageQuotaReservation :execresult
DELETE FROM workspace_storage_reservations
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id);

-- Expiry only makes a reservation eligible for cleanup. Admission continues
-- to count it until this locking read owns the row and the sweeper removes it.
-- Active aggregate transactions hold the reservation lock and are skipped.
-- name: LockStorageQuotaSweepWorkspace :one
SELECT quota_row.workspace_id
FROM storage_quota_usage quota_row
WHERE quota_row.workspace_id <> 0
  AND EXISTS (
    SELECT 1
    FROM workspace_storage_reservations reservation
    WHERE reservation.workspace_id = quota_row.workspace_id
      AND reservation.expires_at <= sqlc.arg(expires_at)
  )
ORDER BY quota_row.workspace_id
LIMIT 1
FOR UPDATE SKIP LOCKED;

-- name: LockExpiredStorageQuotaReservations :many
SELECT
  id,
  workspace_id,
  reserved_bytes,
  reserved_database_bytes,
  reserved_items,
  reserved_images,
  resource_key,
  expires_at,
  created_at
FROM workspace_storage_reservations
WHERE workspace_id = sqlc.arg(workspace_id)
  AND expires_at <= sqlc.arg(expires_at)
ORDER BY expires_at, id
LIMIT ?
FOR UPDATE SKIP LOCKED;

-- name: UpdateStorageQuotaReservation :execresult
UPDATE workspace_storage_reservations
SET
  reserved_bytes = sqlc.arg(reserved_bytes),
  reserved_database_bytes = sqlc.arg(reserved_database_bytes),
  reserved_items = sqlc.arg(reserved_items),
  reserved_images = sqlc.arg(reserved_images),
  resource_key = sqlc.narg(resource_key),
  expires_at = sqlc.arg(expires_at)
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id);

-- name: BindStorageQuotaReservationResource :execresult
UPDATE workspace_storage_reservations
SET reserved_bytes = 0,
    resource_key = sqlc.arg(resource_key)
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id);

-- name: RebuildStorageQuotaUsageRows :many
SELECT
  w.id AS workspace_id,
  CAST((SELECT COUNT(*) FROM items i WHERE i.workspace_id = w.id) AS UNSIGNED) AS item_count,
  CAST((SELECT COUNT(*) FROM item_images ii JOIN items i ON i.id = ii.item_id WHERE i.workspace_id = w.id) AS UNSIGNED) AS image_count,
  CAST((
    SELECT COALESCE(SUM(accounted.storage_bytes), 0)
    FROM (
      SELECT candidates.workspace_id, candidates.resource_key, MAX(candidates.storage_bytes) AS storage_bytes
      FROM (
        SELECT
          i.workspace_id,
          SUBSTRING(ii.image_url, CHAR_LENGTH('/static/uploads/') + 1) AS resource_key,
          ii.storage_bytes
        FROM item_images ii
        JOIN items i ON i.id = ii.item_id
        WHERE ii.image_url LIKE '/static/uploads/%'
        UNION ALL
        SELECT workspace_id, resource_key, storage_bytes
        FROM resource_cleanup_outbox
        WHERE kind = 'upload_blob' AND storage_bytes > 0
      ) candidates
      GROUP BY candidates.workspace_id, candidates.resource_key
    ) accounted
    WHERE accounted.workspace_id = w.id
  ) AS UNSIGNED) AS upload_blob_bytes,
  CAST((
    SELECT
      COALESCE(SUM(OCTET_LENGTH(orun.original_hocr) + OCTET_LENGTH(orun.original_text)), 0)
    FROM ocr_runs orun
    JOIN item_images ii ON ii.id = orun.item_image_id
    JOIN items i ON i.id = ii.item_id
    WHERE i.workspace_id = w.id
  ) + (
    SELECT COALESCE(SUM(OCTET_LENGTH(ap.payload)), 0)
    FROM annotation_pages ap
    WHERE ap.workspace_id = w.id
  ) + (
    SELECT COALESCE(SUM(OCTET_LENGTH(a.payload)), 0)
    FROM annotations a
    WHERE a.workspace_id = w.id
  ) + (
    SELECT COALESCE(SUM(OCTET_LENGTH(pap.payload)), 0)
    FROM published_annotation_pages pap
    WHERE pap.workspace_id = w.id
  ) + (
    SELECT COALESCE(SUM(OCTET_LENGTH(i.source_manifest)), 0)
    FROM items i
    WHERE i.workspace_id = w.id
  ) + (
    SELECT COALESCE(SUM(pca.database_bytes), 0)
    FROM provider_call_audits pca
    WHERE pca.workspace_id = w.id
  ) AS UNSIGNED) AS database_bytes,
  CAST((
    SELECT COALESCE(SUM(r.reserved_bytes), 0)
    FROM workspace_storage_reservations r
    WHERE r.workspace_id = w.id
  ) AS UNSIGNED) AS reserved_upload_blob_bytes,
  CAST((
    SELECT COALESCE(SUM(r.reserved_database_bytes), 0)
    FROM workspace_storage_reservations r
    WHERE r.workspace_id = w.id
  ) AS UNSIGNED) AS reserved_database_bytes,
  CAST((
    SELECT COALESCE(SUM(r.reserved_items), 0)
    FROM workspace_storage_reservations r
    WHERE r.workspace_id = w.id
  ) AS UNSIGNED) AS reserved_item_count,
  CAST((
    SELECT COALESCE(SUM(r.reserved_images), 0)
    FROM workspace_storage_reservations r
    WHERE r.workspace_id = w.id
  ) AS UNSIGNED) AS reserved_image_count
FROM (
  SELECT id
  FROM workspaces
  UNION
  SELECT workspace_id AS id
  FROM workspace_storage_reservations
  UNION
  SELECT workspace_id AS id
  FROM resource_cleanup_outbox
  WHERE workspace_id > 0
) w
ORDER BY w.id;

-- name: GetItemImageDurableDatabaseBytes :one
SELECT CAST(COALESCE(SUM(payloads.byte_count), 0) AS UNSIGNED) AS database_bytes
FROM (
  SELECT OCTET_LENGTH(orun.original_hocr) + OCTET_LENGTH(orun.original_text) AS byte_count
  FROM ocr_runs orun
  WHERE orun.item_image_id = sqlc.arg(item_image_id)
  UNION ALL
  SELECT OCTET_LENGTH(ap.payload)
  FROM annotation_pages ap
  WHERE ap.workspace_id = sqlc.arg(workspace_id)
    AND ap.item_image_id = sqlc.arg(item_image_id)
  UNION ALL
  SELECT OCTET_LENGTH(a.payload)
  FROM annotations a
  WHERE a.workspace_id = sqlc.arg(workspace_id)
    AND a.item_image_id = sqlc.arg(item_image_id)
  UNION ALL
  SELECT OCTET_LENGTH(pap.payload)
  FROM published_annotation_pages pap
  WHERE pap.workspace_id = sqlc.arg(workspace_id)
    AND pap.item_image_id = sqlc.arg(item_image_id)
  UNION ALL
  SELECT pca.database_bytes
  FROM provider_call_audits pca
  JOIN item_images audit_ii ON audit_ii.id = pca.item_image_id
  WHERE pca.workspace_id = sqlc.arg(workspace_id)
    AND audit_ii.id = sqlc.arg(item_image_id)
) payloads;

-- name: GetItemDurableDatabaseBytes :one
SELECT CAST(COALESCE(SUM(payloads.byte_count), 0) AS UNSIGNED) AS database_bytes
FROM (
  SELECT OCTET_LENGTH(i.source_manifest) AS byte_count
  FROM items i
  WHERE i.workspace_id = sqlc.arg(workspace_id)
    AND i.id = sqlc.arg(item_id)
  UNION ALL
  SELECT OCTET_LENGTH(orun.original_hocr) + OCTET_LENGTH(orun.original_text) AS byte_count
  FROM ocr_runs orun
  JOIN item_images ii ON ii.id = orun.item_image_id
  JOIN items i ON i.id = ii.item_id
  WHERE i.workspace_id = sqlc.arg(workspace_id)
    AND i.id = sqlc.arg(item_id)
  UNION ALL
  SELECT OCTET_LENGTH(ap.payload)
  FROM annotation_pages ap
  JOIN item_images ii ON ii.id = ap.item_image_id
  WHERE ap.workspace_id = sqlc.arg(workspace_id)
    AND ii.item_id = sqlc.arg(item_id)
  UNION ALL
  SELECT OCTET_LENGTH(a.payload)
  FROM annotations a
  JOIN item_images ii ON ii.id = a.item_image_id
  WHERE a.workspace_id = sqlc.arg(workspace_id)
    AND ii.item_id = sqlc.arg(item_id)
  UNION ALL
  SELECT OCTET_LENGTH(pap.payload)
  FROM published_annotation_pages pap
  JOIN item_images ii ON ii.id = pap.item_image_id
  WHERE pap.workspace_id = sqlc.arg(workspace_id)
    AND ii.item_id = sqlc.arg(item_id)
  UNION ALL
  SELECT pca.database_bytes
  FROM provider_call_audits pca
  JOIN item_images ii ON ii.id = pca.item_image_id
  WHERE pca.workspace_id = sqlc.arg(workspace_id)
    AND ii.item_id = sqlc.arg(item_id)
) payloads;

-- name: ReplaceStorageQuotaUsage :exec
INSERT INTO storage_quota_usage (
  workspace_id,
  upload_blob_bytes,
  database_bytes,
  item_count,
  image_count,
  reserved_upload_blob_bytes,
  reserved_database_bytes,
  reserved_item_count,
  reserved_image_count
) VALUES (
  sqlc.arg(workspace_id),
  sqlc.arg(upload_blob_bytes),
  sqlc.arg(database_bytes),
  sqlc.arg(item_count),
  sqlc.arg(image_count),
  sqlc.arg(reserved_upload_blob_bytes),
  sqlc.arg(reserved_database_bytes),
  sqlc.arg(reserved_item_count),
  sqlc.arg(reserved_image_count)
)
ON DUPLICATE KEY UPDATE
  upload_blob_bytes = VALUES(upload_blob_bytes),
  database_bytes = VALUES(database_bytes),
  item_count = VALUES(item_count),
  image_count = VALUES(image_count),
  reserved_upload_blob_bytes = VALUES(reserved_upload_blob_bytes),
  reserved_database_bytes = VALUES(reserved_database_bytes),
  reserved_item_count = VALUES(reserved_item_count),
  reserved_image_count = VALUES(reserved_image_count);

-- name: DeleteOrphanStorageQuotaUsage :exec
DELETE FROM storage_quota_usage
WHERE workspace_id <> 0
  AND upload_blob_bytes = 0
  AND database_bytes = 0
  AND item_count = 0
  AND image_count = 0
  AND reserved_upload_blob_bytes = 0
  AND reserved_database_bytes = 0
  AND reserved_item_count = 0
  AND reserved_image_count = 0
  AND workspace_id NOT IN (SELECT id FROM workspaces)
  AND workspace_id NOT IN (SELECT workspace_id FROM workspace_storage_reservations)
  AND workspace_id NOT IN (SELECT workspace_id FROM resource_cleanup_outbox WHERE workspace_id > 0);
