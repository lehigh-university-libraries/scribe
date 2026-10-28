-- Immutable source geometry and derived correction telemetry belong to the
-- OCR run. Ground truth is an explicit snapshot, never another editable page.
ALTER TABLE ocr_runs
  ADD COLUMN original_page LONGTEXT NULL,
  ADD COLUMN segmentation_correction LONGTEXT NULL;

CREATE TABLE annotation_ground_truth (
  workspace_id BIGINT UNSIGNED NOT NULL,
  item_image_id BIGINT UNSIGNED NOT NULL,
  revision BIGINT UNSIGNED NOT NULL,
  context_id BIGINT UNSIGNED NULL,
  document_type VARCHAR(64) NOT NULL,
  payload LONGTEXT NOT NULL,
  original_page LONGTEXT NOT NULL,
  image_url VARCHAR(2048) NOT NULL,
  transcript LONGTEXT NOT NULL,
  marked_by_user_id BIGINT UNSIGNED NULL,
  marked_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (workspace_id, item_image_id, revision),
  INDEX idx_ground_truth_context (workspace_id, context_id, item_image_id),
  CONSTRAINT chk_ground_truth_revision CHECK (revision > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
