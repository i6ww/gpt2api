-- High-concurrency generation safeguards.
-- 1) ClaimBatch needs indexes that match provider/status/lease predicates.
-- 2) OpenAI per-key queue guard counts active tasks by from_api_key_id/status.
-- 3) account_lease is a short-lived distributed lease table for multi-agent account exclusivity.

ALTER TABLE generation_task
  ADD INDEX idx_claim_pending_provider (status, provider, claim_node_id, id),
  ADD INDEX idx_claim_expired_provider (status, claim_lease_until, provider, id),
  ADD INDEX idx_api_key_status (from_api_key_id, status, id);

CREATE TABLE IF NOT EXISTS account_lease (
  provider varchar(32) NOT NULL,
  account_id bigint unsigned NOT NULL,
  slot_no int NOT NULL DEFAULT 1,
  task_id char(26) NOT NULL,
  holder varchar(64) NOT NULL DEFAULT '',
  lease_until datetime(3) NOT NULL,
  created_at datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (provider, account_id, slot_no),
  KEY idx_task_id (task_id),
  KEY idx_lease_until (lease_until)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
