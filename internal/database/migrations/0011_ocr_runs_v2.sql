-- Link ocr_runs to item_images and the context that was used to produce them.
-- session_id is retained for the data migration window and will be dropped
-- once all callers reference item_image_id instead.
ALTER TABLE ocr_runs
  ADD COLUMN IF NOT EXISTS item_image_id BIGINT UNSIGNED NULL AFTER session_id,
  ADD COLUMN IF NOT EXISTS context_id    BIGINT UNSIGNED NULL AFTER item_image_id;

ALTER TABLE ocr_runs
  ADD INDEX IF NOT EXISTS idx_ocr_runs_image (item_image_id);

-- Backfill item_image_id from the item_images rows created in migration 0008
UPDATE ocr_runs o
  JOIN item_images ii ON ii.item_id = o.session_id AND ii.sequence = 0
  SET o.item_image_id = ii.id
  WHERE o.item_image_id IS NULL;
