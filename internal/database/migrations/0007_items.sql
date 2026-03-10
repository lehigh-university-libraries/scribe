CREATE TABLE IF NOT EXISTS items (
  id          VARCHAR(64) PRIMARY KEY,
  user_id     BIGINT UNSIGNED NOT NULL DEFAULT 1,
  name        VARCHAR(255) NOT NULL,
  source_type ENUM('url', 'upload', 'manifest') NOT NULL DEFAULT 'url',
  source_url  TEXT NULL,
  metadata    JSON NULL,
  created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_items_user (user_id)
);

-- Migrate existing sessions into items (idempotent via PRIMARY KEY conflict)
INSERT IGNORE INTO items (id, user_id, name, source_type, created_at, updated_at)
  SELECT id, 1, name, 'url', created_at, updated_at
  FROM sessions;
