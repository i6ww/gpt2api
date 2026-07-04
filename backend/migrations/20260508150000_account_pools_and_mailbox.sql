-- +goose Up
-- +goose StatementBegin

-- =============================================================
-- 号池管理 + 共享邮箱池
-- 设计要点：
--   1) 全局代理 / 全局邮箱：号池表不内嵌 proxy / outlook 字段
--   2) mail_pool 单独建表，三个号池注册时按需 acquire/release
--   3) 三张独立号池表（pool_adobe / pool_grok / pool_gpt），
--      每张表只放各 provider 真正特有的字段
--   4) 敏感字段（password / token / cookie / sso）一律 AES-256-GCM 落盘
-- =============================================================

-- 1. 共享邮箱池
CREATE TABLE IF NOT EXISTS `mail_pool` (
  `id`                 BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `email`              VARCHAR(255) NOT NULL,
  `password_enc`       BLOB         NOT NULL                              COMMENT 'AES-256-GCM 加密的邮箱密码',
  `client_id`          VARCHAR(128) NOT NULL                              COMMENT 'Outlook OAuth client_id',
  `refresh_token_enc`  BLOB         NOT NULL                              COMMENT 'AES-256-GCM 加密的 refresh_token',
  `mode`               VARCHAR(32)  NOT NULL DEFAULT 'outlook_graph'      COMMENT 'outlook_imap / outlook_graph / tempmail / cf',
  `status`             VARCHAR(32)  NOT NULL DEFAULT 'available'          COMMENT 'available / in_use / registered / failed / disabled',
  `failure_count`      INT          NOT NULL DEFAULT 0,
  `last_error`         VARCHAR(500) DEFAULT NULL,
  `used_by_provider`   VARCHAR(32)  DEFAULT NULL                          COMMENT 'adobe / grok / gpt',
  `used_by_account_id` BIGINT UNSIGNED DEFAULT NULL,
  `imported_at`        DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `used_at`            DATETIME(3)  DEFAULT NULL,
  `registered_at`      DATETIME(3)  DEFAULT NULL,
  `created_at`         DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at`         DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at`         DATETIME(3)  DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_mail_email` (`email`),
  KEY `idx_mail_status` (`status`),
  KEY `idx_mail_mode` (`mode`),
  KEY `idx_mail_deleted` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='共享邮箱池（4 段格式 outlook + tempmail + cf）';

-- 2. ADOBE 号池（Firefly）
CREATE TABLE IF NOT EXISTS `pool_adobe` (
  `id`                    BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `email`                 VARCHAR(255) NOT NULL,
  `display_name`          VARCHAR(128) DEFAULT NULL,
  `adobe_user_id`         VARCHAR(64)  DEFAULT NULL                       COMMENT 'Adobe IMS user_id',
  `password_enc`          BLOB         DEFAULT NULL                       COMMENT 'AES-256-GCM 加密 Adobe 密码',
  `access_token_enc`      BLOB         DEFAULT NULL                       COMMENT 'AES-256-GCM 加密 access_token',
  `cookie_enc`            BLOB         DEFAULT NULL                       COMMENT 'AES-256-GCM 加密 cookie',
  `status`                VARCHAR(32)  NOT NULL DEFAULT 'valid'           COMMENT 'valid / invalid / disabled / cooldown',
  `source`                VARCHAR(32)  NOT NULL DEFAULT 'register'        COMMENT 'register / import',
  `credits`               DECIMAL(12,2) NOT NULL DEFAULT 0                COMMENT 'Firefly 积分余额',
  `expires_at`            DATETIME(3)  DEFAULT NULL                       COMMENT 'access_token 失效时间',
  `last_checked_at`       DATETIME(3)  DEFAULT NULL,
  `last_credits_check_at` DATETIME(3)  DEFAULT NULL,
  `last_refresh_at`       DATETIME(3)  DEFAULT NULL,
  `last_used_at`          DATETIME(3)  DEFAULT NULL,
  `refresh_enabled`       TINYINT      NOT NULL DEFAULT 1                 COMMENT '0关闭 1启用自动刷新',
  `failure_count`         INT          NOT NULL DEFAULT 0,
  `error_message`         VARCHAR(500) DEFAULT NULL,
  `cooldown_until`        DATETIME(3)  DEFAULT NULL,
  `notes`                 VARCHAR(500) DEFAULT NULL,
  `created_at`            DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at`            DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at`            DATETIME(3)  DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_pool_adobe_email` (`email`),
  KEY `idx_pool_adobe_status` (`status`),
  KEY `idx_pool_adobe_expires` (`expires_at`),
  KEY `idx_pool_adobe_deleted` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='ADOBE Firefly 号池';

-- 3. GROK 号池
CREATE TABLE IF NOT EXISTS `pool_grok` (
  `id`               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `email`            VARCHAR(255) NOT NULL,
  `password_enc`     BLOB         DEFAULT NULL                            COMMENT 'AES-256-GCM 加密 Grok 密码',
  `given_name`       VARCHAR(64)  DEFAULT NULL,
  `family_name`      VARCHAR(64)  DEFAULT NULL,
  `sso_enc`          BLOB         DEFAULT NULL                            COMMENT 'AES-256-GCM 加密 sso',
  `sso_rw_enc`       BLOB         DEFAULT NULL                            COMMENT 'AES-256-GCM 加密 sso-rw',
  `user_agent`       VARCHAR(255) DEFAULT NULL,
  `trial_status`     VARCHAR(32)  NOT NULL DEFAULT 'pending'              COMMENT 'pending / activating / active / failed / expired',
  `trial_started_at` DATETIME(3)  DEFAULT NULL,
  `trial_expires_at` DATETIME(3)  DEFAULT NULL,
  `trial_error`      VARCHAR(500) DEFAULT NULL,
  `payment_url`      VARCHAR(500) DEFAULT NULL                            COMMENT '订阅支付链接',
  `notes`            VARCHAR(500) DEFAULT NULL,
  `registered_at`    DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `created_at`       DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at`       DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at`       DATETIME(3)  DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_pool_grok_email` (`email`),
  KEY `idx_pool_grok_trial` (`trial_status`),
  KEY `idx_pool_grok_expires` (`trial_expires_at`),
  KEY `idx_pool_grok_deleted` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='GROK 号池（含订阅试用状态机）';

-- 4. GPT 号池
CREATE TABLE IF NOT EXISTS `pool_gpt` (
  `id`                BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `email`             VARCHAR(255) NOT NULL,
  `password_enc`      BLOB         DEFAULT NULL                            COMMENT 'AES-256-GCM 加密注册密码',
  `access_token_enc`  BLOB         DEFAULT NULL                            COMMENT 'AES-256-GCM 加密 access_token',
  `refresh_token_enc` BLOB         DEFAULT NULL                            COMMENT 'AES-256-GCM 加密 refresh_token',
  `oauth_issuer`      VARCHAR(255) DEFAULT NULL                            COMMENT 'OAuth Issuer，比如 https://auth.openai.com',
  `oauth_client_id`   VARCHAR(128) DEFAULT NULL,
  `status`            VARCHAR(32)  NOT NULL DEFAULT 'valid'                COMMENT 'valid / invalid / disabled / cooldown',
  `expires_at`        DATETIME(3)  DEFAULT NULL                            COMMENT 'access_token 失效时间',
  `last_checked_at`   DATETIME(3)  DEFAULT NULL,
  `last_refresh_at`   DATETIME(3)  DEFAULT NULL,
  `last_used_at`      DATETIME(3)  DEFAULT NULL,
  `failure_count`     INT          NOT NULL DEFAULT 0,
  `error_message`     VARCHAR(500) DEFAULT NULL,
  `notes`             VARCHAR(500) DEFAULT NULL,
  `registered_at`     DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `created_at`        DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at`        DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at`        DATETIME(3)  DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_pool_gpt_email` (`email`),
  KEY `idx_pool_gpt_status` (`status`),
  KEY `idx_pool_gpt_expires` (`expires_at`),
  KEY `idx_pool_gpt_deleted` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='GPT 号池';

-- 5. 系统配置：邮箱配置默认值（注册流程统一收件源）
INSERT INTO `system_config` (`key`, `value`, `remark`) VALUES
  ('mail.default_backend',  '"outlook_graph"',                                                                         '默认收件后端：outlook_imap / outlook_graph / tempmail / cf'),
  ('mail.poll_timeout_sec', '180',                                                                                     '邮件等待超时（秒）'),
  ('mail.max_failures',     '3',                                                                                       '邮箱单条最大失败次数（达阈值标 failed）'),
  ('mail.outlook',          '{"mode":"graph","scope_imap":"https://outlook.office.com/IMAP.AccessAsUser.All offline_access","scope_graph":"https://graph.microsoft.com/Mail.Read offline_access"}', 'Outlook 收件配置'),
  ('mail.tempmail',         '{"api_base_url":"","new_address_path":"/api/new_address","mails_path":"/api/mails?limit=10&offset=0","address_name":"","address_domains":[]}', '临时邮箱 API 配置'),
  ('mail.cf',               '{"worker_domain":"","email_domain":"","admin_password":""}',                              'Cloudflare Worker 邮箱配置')
ON DUPLICATE KEY UPDATE `value`=VALUES(`value`);

-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS `pool_gpt`;
DROP TABLE IF EXISTS `pool_grok`;
DROP TABLE IF EXISTS `pool_adobe`;
DROP TABLE IF EXISTS `mail_pool`;
DELETE FROM `system_config` WHERE `key` IN (
  'mail.default_backend', 'mail.poll_timeout_sec', 'mail.max_failures',
  'mail.outlook', 'mail.tempmail', 'mail.cf'
);
