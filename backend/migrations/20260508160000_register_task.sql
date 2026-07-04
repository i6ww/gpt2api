-- +goose Up
-- +goose StatementBegin

-- 号池注册任务（异步执行）
CREATE TABLE IF NOT EXISTS `register_task` (
  `id`              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `provider`        VARCHAR(16)  NOT NULL                              COMMENT 'adobe / grok / gpt',
  `status`          VARCHAR(32)  NOT NULL DEFAULT 'pending'             COMMENT 'pending / running / success / failed / cancelled',
  `step`            VARCHAR(64)  DEFAULT NULL                           COMMENT '当前阶段标识，例如 acquire_mail / submit / verify',
  `progress`        TINYINT UNSIGNED NOT NULL DEFAULT 0                  COMMENT '0-100',
  `mail_id`         BIGINT UNSIGNED DEFAULT NULL                        COMMENT 'mail_pool.id，acquired 后写入',
  `email`           VARCHAR(255) DEFAULT NULL                           COMMENT '冗余 email，便于列表展示',
  `payload`         JSON         DEFAULT NULL                           COMMENT '注册参数（first_name / last_name / proxy_id 等）',
  `result`          JSON         DEFAULT NULL                           COMMENT '产出摘要（pool_account_id / has_refresh_token 等）',
  `error`           VARCHAR(500) DEFAULT NULL,
  `pool_account_id` BIGINT UNSIGNED DEFAULT NULL                        COMMENT '成功后写入的号池行 ID（pool_adobe / pool_grok / pool_gpt）',
  `cancel_requested` TINYINT     NOT NULL DEFAULT 0                     COMMENT '0 否 1 已请求取消',
  `created_by`      BIGINT UNSIGNED DEFAULT NULL,
  `created_at`      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `started_at`      DATETIME(3)  DEFAULT NULL,
  `finished_at`     DATETIME(3)  DEFAULT NULL,
  `updated_at`      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at`      DATETIME(3)  DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_register_task_status` (`status`),
  KEY `idx_register_task_provider` (`provider`),
  KEY `idx_register_task_created` (`created_at`),
  KEY `idx_register_task_deleted` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='号池注册任务（异步执行）';

-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS `register_task`;
