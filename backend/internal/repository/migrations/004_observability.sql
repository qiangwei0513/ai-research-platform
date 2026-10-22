CREATE TABLE IF NOT EXISTS task_observations (
  task_id CHAR(32) CHARACTER SET ascii PRIMARY KEY,
  request_id VARCHAR(64) CHARACTER SET ascii NOT NULL,
  FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS task_usage (
  task_id CHAR(32) CHARACTER SET ascii NOT NULL,
  attempt INT NOT NULL,
  data JSON NULL,
  recorded_at DATETIME(6) NOT NULL,
  PRIMARY KEY (task_id, attempt),
  INDEX usage_time (recorded_at),
  FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS worker_heartbeats (
  id CHAR(32) CHARACTER SET ascii PRIMARY KEY,
  last_seen DATETIME(6) NOT NULL,
  concurrency INT NOT NULL,
  active INT NOT NULL,
  INDEX heartbeat_time (last_seen)
) ENGINE=InnoDB;
