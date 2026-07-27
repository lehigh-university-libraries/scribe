CREATE TABLE users (
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

CREATE TABLE workspaces (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  owner_user_id BIGINT UNSIGNED NULL,
  name VARCHAR(255) NOT NULL,
  slug VARCHAR(255) NOT NULL UNIQUE,
  is_personal BOOLEAN NOT NULL DEFAULT FALSE,
  created_by_user_id BIGINT UNSIGNED NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_workspaces_owner (owner_user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE workspace_members (
  workspace_id BIGINT UNSIGNED NOT NULL,
  user_id BIGINT UNSIGNED NOT NULL,
  role ENUM('admin', 'write', 'create', 'read') NOT NULL DEFAULT 'read',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (workspace_id, user_id),
  INDEX idx_workspace_members_user (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Transactionally maintained quota materialization. workspace_id 0 is the
-- global aggregate; positive IDs are tenant aggregates. The table has no
-- parent constraint because the global row is intentional and cleanup accounting
-- must survive workspace deletion until physical bytes are removed.
CREATE TABLE storage_quota_usage (
  workspace_id BIGINT UNSIGNED NOT NULL,
  upload_blob_bytes BIGINT UNSIGNED NOT NULL DEFAULT 0,
  database_bytes BIGINT UNSIGNED NOT NULL DEFAULT 0,
  item_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
  image_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
  reserved_upload_blob_bytes BIGINT UNSIGNED NOT NULL DEFAULT 0,
  reserved_database_bytes BIGINT UNSIGNED NOT NULL DEFAULT 0,
  reserved_item_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
  reserved_image_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  PRIMARY KEY (workspace_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT INTO storage_quota_usage (workspace_id) VALUES (0), (1)
ON DUPLICATE KEY UPDATE workspace_id = VALUES(workspace_id);

INSERT IGNORE INTO workspaces (id, owner_user_id, name, slug, is_personal, created_by_user_id)
VALUES (1, 1, 'Anonymous', 'anonymous', TRUE, 1);

INSERT IGNORE INTO workspace_members (workspace_id, user_id, role)
VALUES (1, 1, 'admin');

CREATE TABLE auth_sessions (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  token_hash CHAR(64) NOT NULL UNIQUE,
  user_id BIGINT UNSIGNED NOT NULL,
  expires_at TIMESTAMP NOT NULL,
  user_agent TEXT NULL,
  ip_address VARCHAR(255) NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  INDEX idx_auth_sessions_user (user_id),
  INDEX idx_auth_sessions_expires_at (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE items (
  id VARCHAR(64) PRIMARY KEY,
  user_id BIGINT UNSIGNED NOT NULL DEFAULT 1,
  workspace_id BIGINT UNSIGNED NOT NULL DEFAULT 1,
  name VARCHAR(255) NOT NULL,
  source_type ENUM('url', 'upload', 'manifest', 'hocr') NOT NULL DEFAULT 'url',
  source_url TEXT NULL,
  source_manifest LONGTEXT NULL,
  metadata JSON NOT NULL DEFAULT ('{}'),
  external_reference_id VARCHAR(512) NOT NULL DEFAULT '',
  caller_idempotency_key VARCHAR(256) NOT NULL DEFAULT '',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_items_user (user_id),
  INDEX idx_items_workspace_created (workspace_id, created_at, id),
  INDEX idx_items_workspace_external_reference (workspace_id, external_reference_id),
  UNIQUE KEY uq_items_workspace_id (workspace_id, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE item_images (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  workspace_id BIGINT UNSIGNED NOT NULL,
  item_id VARCHAR(64) NOT NULL,
  sequence INT UNSIGNED NOT NULL DEFAULT 0,
  image_url TEXT NOT NULL,
  -- Exact physical bytes for Scribe-owned immutable uploads; external image
  -- URLs are zero because Scribe does not own their blob lifecycle.
  storage_bytes BIGINT UNSIGNED NOT NULL DEFAULT 0,
  canvas_uri VARCHAR(1024) NULL,
  width INT UNSIGNED NULL,
  height INT UNSIGNED NULL,
  label VARCHAR(255) NULL,
  hocr_url TEXT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  UNIQUE KEY uq_item_images_item_seq (item_id, sequence),
  UNIQUE KEY uq_item_images_workspace_id (workspace_id, id),
  INDEX idx_item_images_canvas (canvas_uri(255)),
  INDEX idx_item_images_item (item_id),
  INDEX idx_item_images_image_url (image_url(255))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

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

CREATE TABLE workspace_storage_reservations (
  id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
  workspace_id BIGINT UNSIGNED NOT NULL,
  reserved_bytes BIGINT UNSIGNED NOT NULL DEFAULT 0,
  reserved_database_bytes BIGINT UNSIGNED NOT NULL DEFAULT 0,
  reserved_items INT UNSIGNED NOT NULL DEFAULT 0,
  reserved_images INT UNSIGNED NOT NULL DEFAULT 0,
  -- Bound after immutable naming and before the first blob write. Once bound,
  -- upload bytes are represented by resource_cleanup_outbox instead of this
  -- reservation so crash recovery has an exact object identity.
  resource_key VARCHAR(255) NULL,
  expires_at DATETIME(6) NOT NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  INDEX idx_workspace_storage_reservations_expiry (expires_at),
  INDEX idx_workspace_storage_reservations_workspace (workspace_id, expires_at),
  INDEX idx_workspace_storage_reservations_resource (resource_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE ocr_runs (
  session_id VARCHAR(128) PRIMARY KEY,
  workspace_id BIGINT UNSIGNED NOT NULL,
  item_image_id BIGINT UNSIGNED NOT NULL,
  context_id BIGINT UNSIGNED NULL,
  context_scope_id BIGINT UNSIGNED NULL,
  image_url TEXT NOT NULL,
  provider VARCHAR(64) NOT NULL DEFAULT 'unknown',
  model VARCHAR(255) NOT NULL,
  original_hocr LONGTEXT NOT NULL,
  original_text LONGTEXT NOT NULL,
  canonical_revision BIGINT UNSIGNED NULL,
  levenshtein_distance INT NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_ocr_runs_image (item_image_id),
  INDEX idx_ocr_runs_context (context_id),
  INDEX idx_ocr_runs_context_image (context_id, item_image_id),
  UNIQUE KEY uq_ocr_runs_session_image (session_id, item_image_id),
  UNIQUE KEY uq_ocr_runs_workspace_session (workspace_id, session_id),
  CONSTRAINT chk_ocr_runs_context_owner CHECK (
    (context_id IS NULL AND context_scope_id IS NULL)
    OR (context_id IS NOT NULL AND context_scope_id IN (0, workspace_id))
  )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- OCR runs are append-only baselines. This pointer is the sole mutable answer
-- to "which run is current" and avoids timestamp-order races between runs
-- created in the same second.
CREATE TABLE current_ocr_runs (
  item_image_id BIGINT UNSIGNED NOT NULL,
  session_id VARCHAR(128) NOT NULL,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (item_image_id),
  UNIQUE KEY uq_current_ocr_runs_session (session_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE annotation_pages (
  workspace_id BIGINT UNSIGNED NOT NULL,
  item_image_id BIGINT UNSIGNED NOT NULL,
  page_id VARCHAR(512) NOT NULL,
  canvas_uri VARCHAR(2048) NOT NULL,
  payload LONGTEXT NOT NULL,
  revision BIGINT UNSIGNED NOT NULL DEFAULT 1,
  updated_by_user_id BIGINT UNSIGNED NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (workspace_id, item_image_id),
  UNIQUE KEY uq_annotation_pages_page_id (page_id),
  INDEX idx_annotation_pages_canvas (workspace_id, canvas_uri(255)),
  CONSTRAINT chk_annotation_pages_revision CHECK (revision > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- annotations is a query index derived transactionally from annotation_pages.
-- The complete AnnotationPage JSON above is the only canonical editor state.
CREATE TABLE annotations (
  workspace_id BIGINT UNSIGNED NOT NULL,
  item_image_id BIGINT UNSIGNED NOT NULL,
  id VARCHAR(512) NOT NULL,
  canvas_uri VARCHAR(2048) NOT NULL,
  text_granularity VARCHAR(32) NULL,
  position INT UNSIGNED NOT NULL,
  payload LONGTEXT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (workspace_id, item_image_id, id),
  INDEX idx_annotations_page_order (workspace_id, item_image_id, position),
  INDEX idx_annotations_canvas (workspace_id, canvas_uri(255))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- The public IIIF representation is an explicit snapshot of one committed
-- canonical revision. Draft saves never mutate this row. Item/image deletion
-- removes the published resource in the same explicit application transaction.
CREATE TABLE published_annotation_pages (
  workspace_id BIGINT UNSIGNED NOT NULL,
  item_image_id BIGINT UNSIGNED NOT NULL,
  page_id VARCHAR(512) NOT NULL,
  canvas_uri VARCHAR(2048) NOT NULL,
  payload LONGTEXT NOT NULL,
  published_revision BIGINT UNSIGNED NOT NULL,
  published_by_user_id BIGINT UNSIGNED NULL,
  published_at DATETIME(6) NOT NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  PRIMARY KEY (workspace_id, item_image_id),
  UNIQUE KEY uq_published_annotation_pages_page_id (page_id),
  UNIQUE KEY uq_published_annotation_pages_item_image (item_image_id),
  CONSTRAINT chk_published_annotation_pages_revision CHECK (published_revision > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Durable, coalescing delivery queue for the optional Triplet Presentation
-- mirror. There is at most one row per image; publishing a newer canonical
-- revision replaces any older pending or in-flight payload. Worker completion
-- is fenced by both revision and lease owner so an older PUT can never discard
-- a newer revision that still needs to be delivered.
CREATE TABLE annotation_mirror_outbox (
  item_image_id BIGINT UNSIGNED NOT NULL,
  revision BIGINT UNSIGNED NOT NULL,
  payload LONGTEXT NOT NULL,
  status ENUM('pending','processing','failed') NOT NULL DEFAULT 'pending',
  attempt_count INT NOT NULL DEFAULT 0,
  max_attempts INT NOT NULL DEFAULT 20,
  next_attempt_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  lease_until DATETIME NULL,
  locked_by VARCHAR(128) NULL,
  last_error TEXT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (item_image_id),
  INDEX idx_annotation_mirror_claim (status, next_attempt_at, lease_until)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Durable standalone-Annotation deletion intent. Replacing an AnnotationPage
-- makes removed children unreachable from the graph, but Triplet stores each
-- child resource independently. The compact JSON array survives a crash after
-- the parent page is replaced and is drained only after every stale child is
-- confirmed absent from Triplet. Repository transactions own the relationship;
-- no database-enforced relationship or cascade is used.
CREATE TABLE annotation_mirror_tombstones (
  item_image_id BIGINT UNSIGNED NOT NULL,
  generation BIGINT UNSIGNED NOT NULL DEFAULT 1,
  annotation_ids LONGTEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (item_image_id),
  CONSTRAINT chk_annotation_mirror_tombstones_json CHECK (JSON_VALID(annotation_ids))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- External resources cannot participate in application transactions. This
-- outbox owns both terminal cleanup and the item-scoped aggregate Manifest
-- projection. Work is intentionally detached from item_images: the row must
-- survive deletion of both its item and, if necessary, its workspace.
-- Re-enqueueing the same resource advances a generation and schedules the
-- replacement behind an active lease, fencing late writes from an older worker.
CREATE TABLE resource_cleanup_outbox (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  kind ENUM('upload_blob','triplet_presentation_image','triplet_presentation_item') NOT NULL,
  resource_key VARCHAR(255) NOT NULL,
  -- Accounting metadata deliberately has no parent constraint. A cleanup row must
  -- survive deletion of both its item and, if necessary, its workspace.
  workspace_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
  storage_bytes BIGINT UNSIGNED NOT NULL DEFAULT 0,
  generation BIGINT UNSIGNED NOT NULL DEFAULT 1,
  -- Once set, reference-creating transactions must reject this immutable
  -- identity until the claimed worker has finished deleting the blob and the
  -- outbox row. The tombstone survives retries and lease recovery.
  delete_fenced_at DATETIME NULL,
  status ENUM('pending','processing','failed') NOT NULL DEFAULT 'pending',
  attempt_count INT NOT NULL DEFAULT 0,
  max_attempts INT NOT NULL DEFAULT 20,
  next_attempt_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  lease_until DATETIME NULL,
  locked_by VARCHAR(128) NULL,
  last_error TEXT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_resource_cleanup_kind_key (kind, resource_key),
  INDEX idx_resource_cleanup_claim (status, next_attempt_at, lease_until),
  INDEX idx_resource_cleanup_quota (kind, workspace_id, storage_bytes)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE contexts (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  user_id BIGINT UNSIGNED NULL,
  workspace_id BIGINT UNSIGNED NULL,
  name VARCHAR(255) NOT NULL,
  description TEXT NULL,
  is_default BOOLEAN NOT NULL DEFAULT FALSE,
  segmentation_model VARCHAR(255) NOT NULL DEFAULT 'tesseract',
  transcription_provider VARCHAR(64) NOT NULL DEFAULT 'ollama',
  transcription_model VARCHAR(255) NOT NULL DEFAULT '',
  temperature DOUBLE NULL,
  system_prompt TEXT NULL,
  scope_id BIGINT UNSIGNED GENERATED ALWAYS AS (COALESCE(workspace_id, 0)) STORED,
  default_scope_id BIGINT UNSIGNED GENERATED ALWAYS AS (
    CASE WHEN is_default THEN COALESCE(workspace_id, 0) ELSE NULL END
  ) STORED,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_contexts_user (user_id),
  INDEX idx_contexts_workspace_default_name (workspace_id, is_default, name),
  INDEX idx_contexts_workspace_default_id (workspace_id, is_default, id),
  UNIQUE KEY uq_contexts_scope_name (scope_id, name),
  UNIQUE KEY uq_contexts_default_scope (default_scope_id),
  UNIQUE KEY uq_contexts_scope_id (scope_id, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE context_selection_rules (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  context_id BIGINT UNSIGNED NOT NULL,
  priority INT NOT NULL DEFAULT 0,
  conditions JSON NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  INDEX idx_rules_context (context_id),
  INDEX idx_rules_priority_id (priority DESC, id ASC)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE transcription_jobs (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  workspace_id BIGINT UNSIGNED NOT NULL,
  item_image_id BIGINT UNSIGNED NOT NULL,
  context_id BIGINT UNSIGNED NULL,
  context_scope_id BIGINT UNSIGNED NULL,
  context_snapshot JSON NOT NULL,
  input_revision BIGINT UNSIGNED NOT NULL,
  status ENUM('pending','running','completed','failed','canceled','superseded') NOT NULL DEFAULT 'pending',
  active_item_image_id BIGINT UNSIGNED GENERATED ALWAYS AS (
    CASE WHEN status IN ('pending', 'running') THEN item_image_id ELSE NULL END
  ) STORED,
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
  error_message VARCHAR(1024) NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_transcription_jobs_active_image (active_item_image_id),
  INDEX idx_transcription_jobs_workspace_created (workspace_id, created_at, id),
  INDEX idx_transcription_jobs_workspace_status (workspace_id, status),
  INDEX idx_transcription_jobs_workspace_image_created (workspace_id, item_image_id, created_at, id),
  INDEX idx_transcription_jobs_pending (status, retry_after, created_at),
  INDEX idx_transcription_jobs_running_lease (status, lease_until, attempt_count, max_attempts),
  INDEX idx_transcription_jobs_owner (id, locked_by, status),
  UNIQUE KEY uq_transcription_jobs_workspace_id (workspace_id, id),
  CONSTRAINT chk_transcription_jobs_context_owner CHECK (
    (context_id IS NULL AND context_scope_id IS NULL)
    OR (context_id IS NOT NULL AND context_scope_id IN (0, workspace_id))
  ),
  CONSTRAINT chk_transcription_jobs_input_revision CHECK (input_revision > 0),
  CONSTRAINT chk_transcription_jobs_attempt_budget CHECK (
    max_attempts > 0 AND attempt_count >= 0 AND attempt_count <= max_attempts
  ),
  CONSTRAINT chk_transcription_jobs_segment_counts CHECK (
    total_segments >= 0 AND completed_segments >= 0 AND failed_segments >= 0
  ),
  CONSTRAINT chk_transcription_jobs_lease_state CHECK (
    (
      status = 'running'
      AND attempt_count > 0
      AND lease_until IS NOT NULL
      AND locked_by IS NOT NULL
    )
    OR (
      status <> 'running'
      AND lease_until IS NULL
      AND locked_by IS NULL
    )
  )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Every worker claim creates one immutable audit record. Identity, context,
-- input revision, owner, token, and start time are write-once; the only update
-- permitted by application queries is the single running-to-terminal outcome
-- transition. The job row remains the queue projection, while these rows are
-- the durable history used to prove retry and fencing behavior.
CREATE TABLE transcription_job_attempts (
  job_id BIGINT UNSIGNED NOT NULL,
  attempt_number INT UNSIGNED NOT NULL,
  context_snapshot JSON NOT NULL,
  input_revision BIGINT UNSIGNED NOT NULL,
  lease_owner VARCHAR(128) NOT NULL,
  lease_token VARCHAR(128) NOT NULL,
  outcome ENUM(
    'running',
    'completed',
    'retryable_failed',
    'failed',
    'canceled',
    'superseded',
    'lease_expired'
  ) NOT NULL DEFAULT 'running',
  safe_error_message VARCHAR(1024) NULL,
  result_revision BIGINT UNSIGNED NULL,
  started_at DATETIME(6) NOT NULL,
  finished_at DATETIME(6) NULL,
  PRIMARY KEY (job_id, attempt_number),
  UNIQUE KEY uq_transcription_job_attempts_token (lease_token),
  INDEX idx_transcription_job_attempts_outcome_finished (outcome, finished_at),
  CONSTRAINT chk_transcription_job_attempt_number CHECK (attempt_number > 0),
  CONSTRAINT chk_transcription_job_attempt_input_revision CHECK (input_revision > 0),
  CONSTRAINT chk_transcription_job_attempt_terminal CHECK (
    (outcome = 'running' AND finished_at IS NULL AND result_revision IS NULL)
    OR (outcome <> 'running' AND finished_at IS NOT NULL)
  ),
  CONSTRAINT chk_transcription_job_attempt_result CHECK (
    (outcome = 'completed' AND result_revision IS NOT NULL)
    OR (outcome <> 'completed' AND result_revision IS NULL)
  ),
  CONSTRAINT chk_transcription_job_attempt_error CHECK (
    (outcome IN ('running', 'completed') AND safe_error_message IS NULL)
    OR (outcome NOT IN ('running', 'completed') AND safe_error_message IS NOT NULL)
  ),
  CONSTRAINT chk_transcription_job_attempt_time CHECK (
    finished_at IS NULL OR finished_at >= started_at
  )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE upload_batches (
  workspace_id BIGINT UNSIGNED NOT NULL,
  id VARCHAR(64) NOT NULL,
  item_id VARCHAR(64) NOT NULL,
  context_id BIGINT UNSIGNED NULL,
  context_scope_id BIGINT UNSIGNED NULL,
  context_snapshot JSON NOT NULL,
  request_hash CHAR(64) NOT NULL,
  creation_token VARCHAR(64) NOT NULL,
  status ENUM('in_progress','completed','canceled') NOT NULL DEFAULT 'in_progress',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (workspace_id, id),
  UNIQUE KEY uq_upload_batches_item (item_id),
  INDEX idx_upload_batches_status (workspace_id, status, updated_at),
  INDEX idx_upload_batches_context (context_id),
  CONSTRAINT chk_upload_batches_context_owner CHECK (
    (context_id IS NULL AND context_scope_id IS NULL)
    OR (context_id IS NOT NULL AND context_scope_id IN (0, workspace_id))
  )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE upload_batch_files (
  workspace_id BIGINT UNSIGNED NOT NULL,
  batch_id VARCHAR(64) NOT NULL,
  sequence INT UNSIGNED NOT NULL,
  filename VARCHAR(255) NOT NULL,
  size BIGINT UNSIGNED NOT NULL,
  content_sha256 CHAR(64) NOT NULL,
  status ENUM('pending','processing','completed','failed','canceled') NOT NULL DEFAULT 'pending',
  attempt_count INT UNSIGNED NOT NULL DEFAULT 0,
  max_attempts INT UNSIGNED NOT NULL DEFAULT 5,
  lease_until DATETIME NULL,
  locked_by VARCHAR(128) NULL,
  item_image_id BIGINT UNSIGNED NULL,
  transcription_job_id BIGINT UNSIGNED NULL,
  error_message TEXT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (workspace_id, batch_id, sequence),
  UNIQUE KEY uq_upload_batch_files_image (item_image_id),
  INDEX idx_upload_batch_files_job (transcription_job_id),
  INDEX idx_upload_batch_files_lease (status, lease_until),
  CONSTRAINT chk_upload_batch_files_size CHECK (size > 0),
  CONSTRAINT chk_upload_batch_files_attempts CHECK (max_attempts > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE provider_call_audits (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  workspace_id BIGINT UNSIGNED NOT NULL,
  session_id VARCHAR(128) NULL,
  item_image_id BIGINT UNSIGNED NULL,
  context_id BIGINT UNSIGNED NULL,
  context_scope_id BIGINT UNSIGNED NULL,
  provider VARCHAR(64) NOT NULL,
  model VARCHAR(255) NOT NULL,
  operation VARCHAR(64) NOT NULL,
  error_message TEXT NULL,
  http_status INT NULL,
  duration_ms BIGINT UNSIGNED NOT NULL DEFAULT 0,
  database_bytes BIGINT UNSIGNED NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  INDEX idx_provider_call_audits_workspace_created (workspace_id, created_at, id),
  INDEX idx_provider_call_audits_item_image (item_image_id),
  INDEX idx_provider_call_audits_session (session_id),
  CONSTRAINT chk_provider_call_audits_context_owner CHECK (
    (context_id IS NULL AND context_scope_id IS NULL)
    OR (context_id IS NOT NULL AND context_scope_id IN (0, workspace_id))
  ),
  CONSTRAINT chk_provider_call_audits_provider
    CHECK (CHAR_LENGTH(TRIM(provider)) > 0),
  CONSTRAINT chk_provider_call_audits_operation
    CHECK (CHAR_LENGTH(TRIM(operation)) > 0),
  CONSTRAINT chk_provider_call_audits_database_bytes
    CHECK (database_bytes >= 512),
  CONSTRAINT chk_provider_call_audits_http_status
    CHECK (http_status IS NULL OR http_status BETWEEN 100 AND 599)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE provider_secrets (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  user_id BIGINT UNSIGNED NULL,
  workspace_id BIGINT UNSIGNED NOT NULL,
  provider VARCHAR(64) NOT NULL,
  name VARCHAR(255) NOT NULL,
  vault_path VARCHAR(512) NOT NULL,
  key_hint VARCHAR(255) NULL,
  lifecycle_state ENUM('pending_write', 'active', 'cleanup_pending') NOT NULL DEFAULT 'pending_write',
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  INDEX idx_provider_secrets_user (user_id),
  INDEX idx_provider_secrets_workspace_provider (workspace_id, provider, updated_at),
  INDEX idx_provider_secrets_cleanup (lifecycle_state, created_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE api_keys (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  workspace_id BIGINT UNSIGNED NOT NULL,
  created_by_user_id BIGINT UNSIGNED NOT NULL,
  name VARCHAR(255) NOT NULL,
  key_prefix VARCHAR(32) NOT NULL,
  key_hash CHAR(64) NOT NULL UNIQUE,
  role ENUM('admin', 'write', 'create', 'read') NOT NULL DEFAULT 'read',
  scopes JSON NULL,
  expires_at TIMESTAMP NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_api_keys_workspace (workspace_id),
  INDEX idx_api_keys_creator (created_by_user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE external_requests (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  workspace_id BIGINT UNSIGNED NOT NULL,
  source VARCHAR(64) NOT NULL,
  idempotency_key VARCHAR(128) NOT NULL,
  request_hash CHAR(64) NOT NULL,
  status ENUM('in_progress','completed','failed') NOT NULL DEFAULT 'in_progress',
  item_id VARCHAR(64) NULL,
  item_image_id BIGINT UNSIGNED NULL,
  transcription_job_id BIGINT UNSIGNED NULL,
  session_id VARCHAR(128) NULL,
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
  INDEX idx_external_requests_session (session_id),
  INDEX idx_external_requests_status_lease (status, lease_until)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE event_outbox (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  event_id VARCHAR(128) NOT NULL,
  event_type VARCHAR(255) NOT NULL,
  workspace_id BIGINT UNSIGNED NULL,
  subject VARCHAR(1024) NULL,
  body_json LONGTEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_event_outbox_event_id (event_id),
  INDEX idx_event_outbox_workspace_id (workspace_id, id),
  INDEX idx_event_outbox_type_created (event_type, created_at),
  INDEX idx_event_outbox_created (created_at)
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
CREATE TABLE webhook_deliveries (
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
