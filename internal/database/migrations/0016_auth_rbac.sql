ALTER TABLE users
  ADD COLUMN IF NOT EXISTS email          VARCHAR(320) NULL AFTER name,
  ADD COLUMN IF NOT EXISTS google_subject VARCHAR(255) NULL AFTER email,
  ADD COLUMN IF NOT EXISTS picture_url    TEXT NULL AFTER google_subject,
  ADD COLUMN IF NOT EXISTS is_admin       BOOLEAN NOT NULL DEFAULT FALSE AFTER picture_url,
  ADD COLUMN IF NOT EXISTS last_login_at  TIMESTAMP NULL AFTER created_at,
  ADD COLUMN IF NOT EXISTS updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP AFTER last_login_at;

CREATE TABLE IF NOT EXISTS auth_sessions (
  id           BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  token_hash   CHAR(64) NOT NULL UNIQUE,
  user_id      BIGINT UNSIGNED NOT NULL,
  expires_at   TIMESTAMP NOT NULL,
  user_agent   TEXT NULL,
  ip_address   VARCHAR(255) NULL,
  last_seen_at TIMESTAMP NULL,
  created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  INDEX idx_auth_sessions_user (user_id),
  INDEX idx_auth_sessions_expires_at (expires_at)
);

CREATE TABLE IF NOT EXISTS organizations (
  id                 BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  name               VARCHAR(255) NOT NULL,
  slug               VARCHAR(255) NOT NULL UNIQUE,
  created_by_user_id BIGINT UNSIGNED NULL,
  created_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS organization_members (
  organization_id BIGINT UNSIGNED NOT NULL,
  user_id         BIGINT UNSIGNED NOT NULL,
  role            ENUM('admin', 'write', 'create', 'read') NOT NULL DEFAULT 'read',
  created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (organization_id, user_id),
  INDEX idx_org_members_user (user_id)
);

CREATE TABLE IF NOT EXISTS workspaces (
  id                 BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  organization_id    BIGINT UNSIGNED NULL,
  owner_user_id      BIGINT UNSIGNED NULL,
  name               VARCHAR(255) NOT NULL,
  slug               VARCHAR(255) NOT NULL UNIQUE,
  is_personal        BOOLEAN NOT NULL DEFAULT FALSE,
  created_by_user_id BIGINT UNSIGNED NULL,
  created_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_workspaces_org (organization_id),
  INDEX idx_workspaces_owner (owner_user_id)
);

CREATE TABLE IF NOT EXISTS workspace_members (
  workspace_id BIGINT UNSIGNED NOT NULL,
  user_id      BIGINT UNSIGNED NOT NULL,
  role         ENUM('admin', 'write', 'create', 'read') NOT NULL DEFAULT 'read',
  created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (workspace_id, user_id),
  INDEX idx_workspace_members_user (user_id)
);

CREATE TABLE IF NOT EXISTS provider_secrets (
  id           BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  user_id      BIGINT UNSIGNED NULL,
  workspace_id BIGINT UNSIGNED NULL,
  provider     VARCHAR(64) NOT NULL,
  name         VARCHAR(255) NOT NULL,
  vault_path   VARCHAR(512) NOT NULL,
  key_hint     VARCHAR(255) NULL,
  created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_provider_secrets_user (user_id),
  INDEX idx_provider_secrets_workspace (workspace_id)
);

INSERT IGNORE INTO workspaces (id, owner_user_id, name, slug, is_personal, created_by_user_id)
VALUES (1, 1, 'Anonymous', 'anonymous', TRUE, 1);

INSERT IGNORE INTO workspace_members (workspace_id, user_id, role)
VALUES (1, 1, 'admin');
