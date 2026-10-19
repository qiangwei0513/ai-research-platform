CREATE TABLE IF NOT EXISTS users (
  id CHAR(32) CHARACTER SET ascii PRIMARY KEY,
  email VARCHAR(254) NOT NULL UNIQUE,
  password_hash VARCHAR(255) NOT NULL,
  created_at DATETIME(6) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS tasks (
  id CHAR(32) CHARACTER SET ascii PRIMARY KEY,
  user_id CHAR(32) CHARACTER SET ascii NOT NULL,
  query TEXT NOT NULL,
  mode VARCHAR(16) NOT NULL,
  status VARCHAR(16) NOT NULL,
  started_at DATETIME(6) NOT NULL,
  finished_at DATETIME(6) NULL,
  deadline_at DATETIME(6) NOT NULL,
  error_code VARCHAR(64) NOT NULL DEFAULT '',
  error_message TEXT NOT NULL,
  version BIGINT NOT NULL DEFAULT 1,
  FOREIGN KEY (user_id) REFERENCES users(id),
  INDEX tasks_user_started (user_id, started_at DESC, id DESC),
  INDEX tasks_expiry (status, deadline_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS agent_runs (
  task_id CHAR(32) CHARACTER SET ascii NOT NULL,
  sequence_no INT NOT NULL,
  stage VARCHAR(32) NOT NULL,
  status VARCHAR(16) NOT NULL,
  started_at DATETIME(6) NOT NULL,
  finished_at DATETIME(6) NOT NULL,
  error_code VARCHAR(64) NOT NULL DEFAULT '',
  PRIMARY KEY (task_id, sequence_no),
  FOREIGN KEY (task_id) REFERENCES tasks(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS reports (
  task_id CHAR(32) CHARACTER SET ascii PRIMARY KEY,
  result MEDIUMTEXT NOT NULL,
  plan JSON NULL,
  search JSON NULL,
  FOREIGN KEY (task_id) REFERENCES tasks(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
