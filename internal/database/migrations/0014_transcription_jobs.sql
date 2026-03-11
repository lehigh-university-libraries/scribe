CREATE TABLE IF NOT EXISTS transcription_jobs (
  id                          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  item_image_id               BIGINT UNSIGNED NOT NULL,
  context_id                  BIGINT UNSIGNED NULL,
  status                      ENUM('pending','running','completed','failed') NOT NULL DEFAULT 'pending',
  total_segments              INT NOT NULL DEFAULT 0,
  completed_segments          INT NOT NULL DEFAULT 0,
  failed_segments             INT NOT NULL DEFAULT 0,
  current_annotation_id       VARCHAR(512) NULL,
  current_annotation_json     LONGTEXT NULL,
  last_result_annotation_json LONGTEXT NULL,
  error_message               TEXT NULL,
  created_at                  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at                  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  INDEX idx_item_image_id (item_image_id),
  INDEX idx_status_created (status, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
