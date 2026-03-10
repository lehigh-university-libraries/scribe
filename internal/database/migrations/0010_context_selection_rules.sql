CREATE TABLE IF NOT EXISTS context_selection_rules (
  id         BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  context_id BIGINT UNSIGNED NOT NULL,
  priority   INT NOT NULL DEFAULT 0,
  conditions JSON NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  INDEX idx_rules_context  (context_id),
  INDEX idx_rules_priority (priority DESC)
);
