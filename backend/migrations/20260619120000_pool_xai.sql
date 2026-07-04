-- +goose Up
-- +goose StatementBegin
--
-- 官方 xAI API 账号池（参考 CLIProxyAPI）。
--
-- 与 pool_grok（grok.com Web SSO cookie 通道）不同，本表存的是 xAI 官方 OAuth
-- 凭证（api.x.ai/v1）：
--
--   credential_enc      = access_token（业务接口真正校验的 Bearer），AES-256-GCM
--   refresh_token_enc   = refresh_token（offline_access），续期调度器用它换新 access_token
--   id_token_enc        = id_token（OIDC，仅用于解析 email / sub 身份）
--   token_endpoint      = OIDC discovery 解析出的 token 端点（grant_type=refresh_token POST 它）
--   base_url            = API base，默认 https://api.x.ai/v1
--
-- access_token 寿命较短（通常 1h），由 xairefresh 调度器在过期前 5min silent refresh，
-- 原地回写 credential_enc + expires_at。OAuth 交互式登录由 cmd/xailogin 一次性完成后导入。
--
CREATE TABLE IF NOT EXISTS `pool_xai` (
  `id`                   BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `email`                VARCHAR(255) NOT NULL,
  `subject`              VARCHAR(128) DEFAULT NULL                          COMMENT 'xAI 用户 ID（id_token 的 sub claim）',
  `credential_enc`       BLOB         NOT NULL                             COMMENT 'AES-256-GCM 加密的 access_token（Bearer）',
  `refresh_token_enc`    BLOB         DEFAULT NULL                          COMMENT 'AES-256-GCM 加密的 refresh_token',
  `id_token_enc`         BLOB         DEFAULT NULL                          COMMENT 'AES-256-GCM 加密的 id_token（OIDC，可选）',
  `token_endpoint`       VARCHAR(255) DEFAULT NULL                          COMMENT 'OAuth token 端点（刷新用）',
  `base_url`             VARCHAR(255) DEFAULT NULL                          COMMENT 'API base，默认 https://api.x.ai/v1',
  `account_type`         VARCHAR(32)  NOT NULL DEFAULT ''                   COMMENT '订阅 tier，free/super_grok/... 暂多为 unknown',
  `status`               VARCHAR(32)  NOT NULL DEFAULT 'valid'              COMMENT 'valid / invalid / disabled / cooldown',
  `source`               VARCHAR(32)  NOT NULL DEFAULT 'import'             COMMENT 'import / register',
  `refresh_enabled`      TINYINT      NOT NULL DEFAULT 1                    COMMENT '0关闭 1启用自动刷新',
  `expires_at`           DATETIME(3)  DEFAULT NULL                          COMMENT 'access_token 失效时间',
  `last_refresh_at`      DATETIME(3)  DEFAULT NULL,
  `last_refresh_result`  VARCHAR(255) DEFAULT NULL,
  `last_used_at`         DATETIME(3)  DEFAULT NULL,
  `last_checked_at`      DATETIME(3)  DEFAULT NULL,
  `proxy_id`             BIGINT UNSIGNED DEFAULT NULL,
  `model_whitelist`      JSON         DEFAULT NULL,
  `weight`               INT          NOT NULL DEFAULT 10,
  `rpm_limit`            INT          NOT NULL DEFAULT 0,
  `tpm_limit`            INT          NOT NULL DEFAULT 0,
  `daily_quota`          INT          NOT NULL DEFAULT 0,
  `monthly_quota`        INT          NOT NULL DEFAULT 0,
  `cooldown_until`       DATETIME(3)  DEFAULT NULL,
  `last_test_at`         DATETIME(3)  DEFAULT NULL,
  `last_test_status`     TINYINT      NOT NULL DEFAULT 0,
  `last_test_latency_ms` INT          NOT NULL DEFAULT 0,
  `last_test_error`      VARCHAR(255) DEFAULT NULL,
  `success_count`        BIGINT UNSIGNED NOT NULL DEFAULT 0,
  `failure_count`        INT          NOT NULL DEFAULT 0,
  `error_message`        VARCHAR(500) DEFAULT NULL,
  `remark`               VARCHAR(255) DEFAULT NULL,
  `notes`                VARCHAR(500) DEFAULT NULL,
  `created_at`           DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at`           DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at`           DATETIME(3)  DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_pool_xai_email` (`email`),
  KEY `idx_pool_xai_status_cd` (`status`, `cooldown_until`),
  KEY `idx_pool_xai_expires` (`expires_at`),
  KEY `idx_pool_xai_deleted` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='官方 xAI API（OAuth）账号池';
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS `pool_xai`;
