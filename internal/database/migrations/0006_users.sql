CREATE TABLE IF NOT EXISTS users (
  id         BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  name       VARCHAR(255) NOT NULL DEFAULT 'anonymous',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Reserve id=1 for anonymous. All unauthenticated operations use this row.
INSERT IGNORE INTO users (id, name) VALUES (1, 'anonymous');
