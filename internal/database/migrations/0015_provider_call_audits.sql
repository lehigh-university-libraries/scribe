CREATE TABLE IF NOT EXISTS provider_call_audits (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  session_id    VARCHAR(128) NULL,
  item_image_id BIGINT UNSIGNED NULL,
  context_id    BIGINT UNSIGNED NULL,
  provider      VARCHAR(64) NOT NULL,
  model         VARCHAR(255) NOT NULL,
  operation     VARCHAR(64) NOT NULL,
  prompt        TEXT NULL,
  request_json  LONGTEXT NULL,
  response_json LONGTEXT NULL,
  error_message TEXT NULL,
  http_status   INT NULL,
  created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  INDEX idx_provider_call_audits_item_image (item_image_id),
  INDEX idx_provider_call_audits_session (session_id),
  INDEX idx_provider_call_audits_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
