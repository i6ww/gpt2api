ALTER TABLE generation_task
  ADD COLUMN poll_retry_after TINYINT UNSIGNED NOT NULL DEFAULT 0
  COMMENT 'last upstream Retry-After seconds for client poll hint'
  AFTER progress;
