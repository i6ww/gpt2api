-- +goose Up
-- +goose StatementBegin

-- 号池注册任务日志（每一步进度 / 警告 / 错误都一行，append-only）
CREATE TABLE IF NOT EXISTS `register_task_log` (
  `id`         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `task_id`    BIGINT UNSIGNED NOT NULL                                COMMENT '关联 register_task.id',
  `provider`   VARCHAR(16)  NOT NULL                                    COMMENT '冗余 provider 便于按家筛',
  `level`      VARCHAR(16)  NOT NULL DEFAULT 'info'                    COMMENT 'info / warn / error',
  `step`       VARCHAR(64)  DEFAULT NULL                                COMMENT '阶段标识，与 register_task.step 同',
  `progress`   TINYINT UNSIGNED DEFAULT NULL                            COMMENT '0-100',
  `message`    VARCHAR(1000) DEFAULT NULL                                COMMENT '日志正文',
  `created_at` DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_register_task_log_task` (`task_id`),
  KEY `idx_register_task_log_created` (`created_at`),
  KEY `idx_register_task_log_provider` (`provider`),
  KEY `idx_register_task_log_level` (`level`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='号池注册任务日志（实时事件流）';

-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS `register_task_log`;
