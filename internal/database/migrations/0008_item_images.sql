CREATE TABLE IF NOT EXISTS item_images (
  id         BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  item_id    VARCHAR(64) NOT NULL,
  sequence   INT UNSIGNED NOT NULL DEFAULT 0,
  image_url  TEXT NOT NULL,
  canvas_uri VARCHAR(1024) NULL,
  label      VARCHAR(255) NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  UNIQUE KEY uq_item_images_item_seq (item_id, sequence),
  INDEX idx_item_images_canvas (canvas_uri(255))
);

-- Migrate existing ocr_runs (each had one image, becomes sequence=0)
INSERT IGNORE INTO item_images (item_id, sequence, image_url, created_at, updated_at)
  SELECT session_id, 0, image_url, created_at, updated_at
  FROM ocr_runs;
