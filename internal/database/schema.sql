CREATE TABLE IF NOT EXISTS sessions (
  id VARCHAR(64) PRIMARY KEY,
  name VARCHAR(255) NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS users (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  name VARCHAR(255) NOT NULL DEFAULT 'anonymous',
  email VARCHAR(320) NULL,
  google_subject VARCHAR(255) NULL,
  picture_url TEXT NULL,
  is_admin BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_login_at TIMESTAMP NULL,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  UNIQUE KEY uq_users_email (email),
  UNIQUE KEY uq_users_google_subject (google_subject)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT IGNORE INTO users (id, name) VALUES (1, 'anonymous');

CREATE TABLE IF NOT EXISTS organizations (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  name VARCHAR(255) NOT NULL,
  slug VARCHAR(255) NOT NULL UNIQUE,
  created_by_user_id BIGINT UNSIGNED NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS organization_members (
  organization_id BIGINT UNSIGNED NOT NULL,
  user_id BIGINT UNSIGNED NOT NULL,
  role ENUM('admin', 'write', 'create', 'read') NOT NULL DEFAULT 'read',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (organization_id, user_id),
  INDEX idx_org_members_user (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS workspaces (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  organization_id BIGINT UNSIGNED NULL,
  owner_user_id BIGINT UNSIGNED NULL,
  name VARCHAR(255) NOT NULL,
  slug VARCHAR(255) NOT NULL UNIQUE,
  is_personal BOOLEAN NOT NULL DEFAULT FALSE,
  created_by_user_id BIGINT UNSIGNED NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_workspaces_org (organization_id),
  INDEX idx_workspaces_owner (owner_user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS workspace_members (
  workspace_id BIGINT UNSIGNED NOT NULL,
  user_id BIGINT UNSIGNED NOT NULL,
  role ENUM('admin', 'write', 'create', 'read') NOT NULL DEFAULT 'read',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (workspace_id, user_id),
  INDEX idx_workspace_members_user (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT IGNORE INTO workspaces (id, owner_user_id, name, slug, is_personal, created_by_user_id)
VALUES (1, 1, 'Anonymous', 'anonymous', TRUE, 1);

INSERT IGNORE INTO workspace_members (workspace_id, user_id, role)
VALUES (1, 1, 'admin');

CREATE TABLE IF NOT EXISTS auth_sessions (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  token_hash CHAR(64) NOT NULL UNIQUE,
  user_id BIGINT UNSIGNED NOT NULL,
  expires_at TIMESTAMP NOT NULL,
  user_agent TEXT NULL,
  ip_address VARCHAR(255) NULL,
  last_seen_at TIMESTAMP NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  INDEX idx_auth_sessions_user (user_id),
  INDEX idx_auth_sessions_expires_at (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS items (
  id VARCHAR(64) PRIMARY KEY,
  user_id BIGINT UNSIGNED NOT NULL DEFAULT 1,
  workspace_id BIGINT UNSIGNED NOT NULL DEFAULT 1,
  name VARCHAR(255) NOT NULL,
  source_type ENUM('url', 'upload', 'manifest') NOT NULL DEFAULT 'url',
  source_url TEXT NULL,
  metadata JSON NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_items_user (user_id),
  INDEX idx_items_workspace_created (workspace_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS item_images (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  item_id VARCHAR(64) NOT NULL,
  sequence INT UNSIGNED NOT NULL DEFAULT 0,
  image_url TEXT NOT NULL,
  canvas_uri VARCHAR(1024) NULL,
  width INT UNSIGNED NULL,
  height INT UNSIGNED NULL,
  label VARCHAR(255) NULL,
  hocr_url TEXT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  UNIQUE KEY uq_item_images_item_seq (item_id, sequence),
  INDEX idx_item_images_canvas (canvas_uri(255)),
  INDEX idx_item_images_item (item_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS ocr_runs (
  session_id VARCHAR(128) PRIMARY KEY,
  item_image_id BIGINT UNSIGNED NULL,
  context_id BIGINT UNSIGNED NULL,
  image_url TEXT NOT NULL,
  provider VARCHAR(64) NOT NULL DEFAULT 'unknown',
  model VARCHAR(255) NOT NULL,
  original_hocr LONGTEXT NOT NULL,
  original_text LONGTEXT NOT NULL,
  corrected_hocr LONGTEXT NULL,
  corrected_text LONGTEXT NULL,
  edit_count INT NOT NULL DEFAULT 0,
  levenshtein_distance INT NOT NULL DEFAULT 0,
  box_edit_count INT NOT NULL DEFAULT 0,
  boxes_added INT NOT NULL DEFAULT 0,
  boxes_deleted INT NOT NULL DEFAULT 0,
  box_change_score DOUBLE NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_ocr_runs_image (item_image_id),
  INDEX idx_ocr_runs_context (context_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS annotations (
  id VARCHAR(512) PRIMARY KEY,
  canvas_uri VARCHAR(1024) NOT NULL,
  payload LONGTEXT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_annotations_canvas_uri (canvas_uri(255))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS contexts (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  user_id BIGINT UNSIGNED NULL,
  workspace_id BIGINT UNSIGNED NULL,
  name VARCHAR(255) NOT NULL,
  description TEXT NULL,
  is_default BOOLEAN NOT NULL DEFAULT FALSE,
  segmentation_model VARCHAR(255) NOT NULL DEFAULT 'tesseract',
  image_preprocessors JSON NULL,
  transcription_provider VARCHAR(64) NOT NULL DEFAULT 'ollama',
  transcription_model VARCHAR(255) NOT NULL DEFAULT '',
  transcription_base_url VARCHAR(2048) NULL,
  transcription_audience VARCHAR(2048) NULL,
  temperature DOUBLE NULL,
  system_prompt TEXT NULL,
  post_processing_steps JSON NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_contexts_user (user_id),
  INDEX idx_contexts_workspace_default_name (workspace_id, is_default, name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS context_selection_rules (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  context_id BIGINT UNSIGNED NOT NULL,
  priority INT NOT NULL DEFAULT 0,
  conditions JSON NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  INDEX idx_rules_context (context_id),
  INDEX idx_rules_priority (priority DESC)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS transcription_jobs (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  item_image_id BIGINT UNSIGNED NOT NULL,
  context_id BIGINT UNSIGNED NULL,
  status ENUM('pending','running','completed','failed') NOT NULL DEFAULT 'pending',
  total_segments INT NOT NULL DEFAULT 0,
  completed_segments INT NOT NULL DEFAULT 0,
  failed_segments INT NOT NULL DEFAULT 0,
  attempt_count INT NOT NULL DEFAULT 0,
  max_attempts INT NOT NULL DEFAULT 3,
  retry_after DATETIME NULL,
  lease_until DATETIME NULL,
  locked_by VARCHAR(128) NULL,
  current_annotation_id VARCHAR(512) NULL,
  current_annotation_json LONGTEXT NULL,
  last_result_annotation_json LONGTEXT NULL,
  error_message TEXT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  INDEX idx_transcription_jobs_item_image (item_image_id),
  INDEX idx_transcription_jobs_pending (status, retry_after, created_at),
  INDEX idx_transcription_jobs_running_lease (status, lease_until, attempt_count, max_attempts),
  INDEX idx_transcription_jobs_owner (id, locked_by, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS provider_call_audits (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  session_id VARCHAR(128) NULL,
  item_image_id BIGINT UNSIGNED NULL,
  context_id BIGINT UNSIGNED NULL,
  provider VARCHAR(64) NOT NULL,
  model VARCHAR(255) NOT NULL,
  operation VARCHAR(64) NOT NULL,
  prompt TEXT NULL,
  request_json LONGTEXT NULL,
  response_json LONGTEXT NULL,
  error_message TEXT NULL,
  http_status INT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  INDEX idx_provider_call_audits_item_image (item_image_id),
  INDEX idx_provider_call_audits_session (session_id),
  INDEX idx_provider_call_audits_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS provider_secrets (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  user_id BIGINT UNSIGNED NULL,
  workspace_id BIGINT UNSIGNED NULL,
  provider VARCHAR(64) NOT NULL,
  name VARCHAR(255) NOT NULL,
  vault_path VARCHAR(512) NOT NULL,
  key_hint VARCHAR(255) NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_provider_secrets_user (user_id),
  INDEX idx_provider_secrets_workspace_provider (workspace_id, provider, updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS api_keys (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  workspace_id BIGINT UNSIGNED NOT NULL,
  created_by_user_id BIGINT UNSIGNED NOT NULL,
  name VARCHAR(255) NOT NULL,
  key_prefix VARCHAR(32) NOT NULL,
  key_hash CHAR(64) NOT NULL UNIQUE,
  role ENUM('admin', 'write', 'create', 'read') NOT NULL DEFAULT 'read',
  scopes JSON NULL,
  last_used_at TIMESTAMP NULL,
  expires_at TIMESTAMP NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_api_keys_workspace (workspace_id),
  INDEX idx_api_keys_creator (created_by_user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS external_requests (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  workspace_id BIGINT UNSIGNED NOT NULL,
  source VARCHAR(64) NOT NULL,
  idempotency_key VARCHAR(128) NOT NULL,
  status ENUM('in_progress','completed','failed') NOT NULL DEFAULT 'in_progress',
  item_id VARCHAR(64) NULL,
  item_image_id BIGINT UNSIGNED NULL,
  transcription_job_id BIGINT UNSIGNED NULL,
  event_header LONGTEXT NULL,
  attempt_count INT NOT NULL DEFAULT 0,
  max_attempts INT NOT NULL DEFAULT 3,
  lease_until DATETIME NULL,
  locked_by VARCHAR(128) NULL,
  error_message TEXT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_external_requests_workspace_source_key (workspace_id, source, idempotency_key),
  INDEX idx_external_requests_item_image (item_image_id),
  INDEX idx_external_requests_job (transcription_job_id),
  INDEX idx_external_requests_status_lease (status, lease_until)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS event_outbox (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  event_id VARCHAR(128) NOT NULL,
  event_type VARCHAR(255) NOT NULL,
  subject VARCHAR(1024) NULL,
  body_json LONGTEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_event_outbox_event_id (event_id),
  INDEX idx_event_outbox_type_created (event_type, created_at),
  INDEX idx_event_outbox_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS webhook_deliveries (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  event_id VARCHAR(128) NOT NULL,
  target_url TEXT NOT NULL,
  target_hash CHAR(64) NOT NULL,
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
  UNIQUE KEY uq_webhook_deliveries_event_target (event_id, target_hash),
  INDEX idx_webhook_deliveries_status_next (status, next_attempt_at),
  INDEX idx_webhook_deliveries_processing_lease (status, lease_until, attempt_count, max_attempts),
  INDEX idx_webhook_deliveries_owner (id, locked_by, status),
  INDEX idx_webhook_deliveries_event_id (event_id),
  INDEX idx_webhook_deliveries_retention (status, updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
