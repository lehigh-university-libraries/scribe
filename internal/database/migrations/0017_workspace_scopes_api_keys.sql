ALTER TABLE items
  ADD COLUMN IF NOT EXISTS workspace_id BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER user_id;

ALTER TABLE contexts
  ADD COLUMN IF NOT EXISTS workspace_id BIGINT UNSIGNED NULL AFTER user_id;

INSERT IGNORE INTO workspaces (owner_user_id, name, slug, is_personal, created_by_user_id)
SELECT u.id, CONCAT('User ', u.id, ' Workspace'), CONCAT('user-', u.id, '-personal'), TRUE, u.id
FROM users u
LEFT JOIN workspaces w ON w.owner_user_id = u.id AND w.is_personal = TRUE
WHERE w.id IS NULL;

INSERT IGNORE INTO workspace_members (workspace_id, user_id, role)
SELECT w.id, w.owner_user_id, 'admin'
FROM workspaces w
WHERE w.is_personal = TRUE AND w.owner_user_id IS NOT NULL;

UPDATE items i
JOIN workspaces w ON w.owner_user_id = i.user_id AND w.is_personal = TRUE
SET i.workspace_id = w.id;

UPDATE contexts c
JOIN workspaces w ON w.owner_user_id = c.user_id AND w.is_personal = TRUE
SET c.workspace_id = w.id
WHERE c.user_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS api_keys (
  id                 BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  workspace_id       BIGINT UNSIGNED NOT NULL,
  created_by_user_id BIGINT UNSIGNED NOT NULL,
  name               VARCHAR(255) NOT NULL,
  key_prefix         VARCHAR(32) NOT NULL,
  key_hash           CHAR(64) NOT NULL UNIQUE,
  role               ENUM('admin', 'write', 'create', 'read') NOT NULL DEFAULT 'read',
  scopes             JSON NULL,
  last_used_at       TIMESTAMP NULL,
  expires_at         TIMESTAMP NULL,
  created_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_api_keys_workspace (workspace_id),
  INDEX idx_api_keys_creator (created_by_user_id)
);
