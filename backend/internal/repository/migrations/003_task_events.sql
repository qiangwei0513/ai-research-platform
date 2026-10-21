CREATE TABLE IF NOT EXISTS task_event_heads (
  task_id CHAR(32) CHARACTER SET ascii PRIMARY KEY,
  last_sequence BIGINT NOT NULL DEFAULT 0,
  FOREIGN KEY (task_id) REFERENCES tasks(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS task_events (
  task_id CHAR(32) CHARACTER SET ascii NOT NULL,
  sequence_no BIGINT NOT NULL,
  event_type VARCHAR(32) NOT NULL,
  payload JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (task_id, sequence_no),
  FOREIGN KEY (task_id) REFERENCES tasks(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
