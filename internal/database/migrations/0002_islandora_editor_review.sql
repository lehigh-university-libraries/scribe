ALTER TABLE items
  ADD COLUMN external_reference_id VARCHAR(512) NOT NULL DEFAULT '',
  ADD COLUMN caller_idempotency_key VARCHAR(256) NOT NULL DEFAULT '',
  ADD INDEX idx_items_workspace_external_reference (workspace_id, external_reference_id);

-- One-time editor handoff grants are minted by a delegated integration only
-- after normal item-image authorization. The URL token is HMAC authenticated;
-- only its digest is persisted so a database read cannot recover a live link.
CREATE TABLE editor_review_tokens (
  id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
  token_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL UNIQUE,
  workspace_id BIGINT UNSIGNED NOT NULL,
  item_id VARCHAR(64) NOT NULL,
  item_image_id BIGINT UNSIGNED NOT NULL,
  issued_by_user_id BIGINT UNSIGNED NOT NULL,
  reviewer_subject_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  reviewer_name VARCHAR(255) NOT NULL,
  reviewer_email VARCHAR(320) NULL,
  session_ttl_seconds INT UNSIGNED NOT NULL,
  expires_at DATETIME(6) NOT NULL,
  redeemed_at DATETIME(6) NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  INDEX idx_editor_review_tokens_workspace_active (workspace_id, expires_at, redeemed_at),
  INDEX idx_editor_review_tokens_image (workspace_id, item_image_id),
  INDEX idx_editor_review_tokens_retention (expires_at, id),
  CONSTRAINT chk_editor_review_tokens_session_ttl
    CHECK (session_ttl_seconds BETWEEN 300 AND 28800),
  CONSTRAINT chk_editor_review_tokens_reviewer_name
    CHECK (CHAR_LENGTH(TRIM(reviewer_name)) > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Redeeming a grant creates an independently revocable, item-image-scoped
-- browser session. It never becomes an ordinary workspace-wide OAuth session.
CREATE TABLE editor_review_sessions (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  token_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL UNIQUE,
  review_token_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL UNIQUE,
  workspace_id BIGINT UNSIGNED NOT NULL,
  item_id VARCHAR(64) NOT NULL,
  item_image_id BIGINT UNSIGNED NOT NULL,
  issued_by_user_id BIGINT UNSIGNED NOT NULL,
  reviewer_subject_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  reviewer_name VARCHAR(255) NOT NULL,
  reviewer_email VARCHAR(320) NULL,
  expires_at DATETIME(6) NOT NULL,
  user_agent VARCHAR(1024) NULL,
  ip_address VARCHAR(255) NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  INDEX idx_editor_review_sessions_workspace_image (workspace_id, item_image_id),
  INDEX idx_editor_review_sessions_issuer (issued_by_user_id),
  INDEX idx_editor_review_sessions_expires_at (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE webhook_subscriptions (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  workspace_id BIGINT UNSIGNED NOT NULL,
  target_url TEXT NOT NULL,
  target_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  signing_secret VARBINARY(1024) NOT NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  PRIMARY KEY (id),
  UNIQUE KEY uq_webhook_subscriptions_workspace_target (workspace_id, target_hash),
  INDEX idx_webhook_subscriptions_workspace (workspace_id, id),
  CONSTRAINT chk_webhook_subscriptions_secret_length
    CHECK (OCTET_LENGTH(signing_secret) BETWEEN 32 AND 1024)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Delivery parentage follows the repository-wide application-owned relationship
-- model: event expansion is serialized with subscription lifecycle on the
-- workspace row, both parent identities are audited after recovery, and every
-- delete path removes deliveries before its event or subscription parent.
--
-- Legacy deliveries came from server configuration and were unsigned. They
-- have neither a subscription identity nor a receiver-known signing secret,
-- so carrying them forward would claim a security transition that did not
-- occur. Keep their durable event_outbox parents, replace the incompatible
-- queue atomically, and require an administrator to create an explicit signed
-- subscription for future events.
CREATE TABLE webhook_deliveries_v2 (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  event_id VARCHAR(128) NOT NULL,
  subscription_id BIGINT UNSIGNED NOT NULL,
  status ENUM('pending','processing','delivered','failed') NOT NULL DEFAULT 'pending',
  attempt_count INT NOT NULL DEFAULT 0,
  max_attempts INT NOT NULL DEFAULT 10,
  next_attempt_at DATETIME NULL,
  lease_until DATETIME NULL,
  locked_by VARCHAR(128) NULL,
  last_error TEXT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_webhook_deliveries_event_subscription (event_id, subscription_id),
  INDEX idx_webhook_deliveries_status_next (status, next_attempt_at),
  INDEX idx_webhook_deliveries_processing_lease (status, lease_until, attempt_count, max_attempts),
  INDEX idx_webhook_deliveries_owner (id, locked_by, status),
  INDEX idx_webhook_deliveries_event_id (event_id),
  INDEX idx_webhook_deliveries_subscription (subscription_id, id),
  INDEX idx_webhook_deliveries_retention (status, updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

RENAME TABLE
  webhook_deliveries TO webhook_deliveries_legacy_unsigned,
  webhook_deliveries_v2 TO webhook_deliveries;

DROP TABLE webhook_deliveries_legacy_unsigned;
