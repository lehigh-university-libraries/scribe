CREATE TABLE IF NOT EXISTS contexts (
  id                     BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  user_id                BIGINT UNSIGNED NULL,
  name                   VARCHAR(255) NOT NULL,
  description            TEXT NULL,
  is_default             BOOLEAN NOT NULL DEFAULT FALSE,
  segmentation_model     VARCHAR(255) NOT NULL DEFAULT 'tesseract',
  image_preprocessors    JSON NULL,
  transcription_provider VARCHAR(64) NOT NULL DEFAULT 'ollama',
  transcription_model    VARCHAR(255) NOT NULL DEFAULT '',
  temperature            DOUBLE NULL,
  system_prompt          TEXT NULL,
  post_processing_steps  JSON NULL,
  created_at             TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at             TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_contexts_user (user_id)
);

-- The application seeds the default system context on startup from env config
-- if no context with is_default=TRUE and user_id IS NULL exists.
