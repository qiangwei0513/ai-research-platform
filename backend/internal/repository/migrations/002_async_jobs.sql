CREATE TABLE IF NOT EXISTS task_jobs (
  task_id CHAR(32) CHARACTER SET ascii PRIMARY KEY,
  user_id CHAR(32) CHARACTER SET ascii NOT NULL,
  request_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  request_hash CHAR(64) CHARACTER SET ascii NOT NULL,
  fresh BOOLEAN NOT NULL DEFAULT FALSE,
  attempt INT NOT NULL DEFAULT 0,
  max_attempts INT NOT NULL DEFAULT 3,
  available_at DATETIME(6) NOT NULL,
  attempt_started_at DATETIME(6) NULL,
  lease_token CHAR(32) CHARACTER SET ascii NOT NULL DEFAULT '',
  lease_until DATETIME(6) NULL,
  FOREIGN KEY (task_id) REFERENCES tasks(id),
  FOREIGN KEY (user_id) REFERENCES users(id),
  UNIQUE KEY jobs_request (user_id, request_key),
  INDEX jobs_lease (lease_until)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS task_outbox (
  task_id CHAR(32) CHARACTER SET ascii PRIMARY KEY,
  next_publish_at DATETIME(6) NOT NULL,
  claim_token CHAR(32) CHARACTER SET ascii NOT NULL DEFAULT '',
  claim_until DATETIME(6) NULL,
  FOREIGN KEY (task_id) REFERENCES tasks(id),
  INDEX outbox_due (next_publish_at, claim_until)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS task_attempts (
  task_id CHAR(32) CHARACTER SET ascii NOT NULL,
  attempt INT NOT NULL,
  status VARCHAR(16) NOT NULL,
  started_at DATETIME(6) NOT NULL,
  finished_at DATETIME(6) NULL,
  error_code VARCHAR(64) NOT NULL DEFAULT '',
  error_message TEXT NOT NULL,
  runs JSON NULL,
  PRIMARY KEY (task_id, attempt),
  FOREIGN KEY (task_id) REFERENCES tasks(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
