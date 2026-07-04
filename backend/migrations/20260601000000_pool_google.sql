-- +goose Up
-- +goose StatementBegin
--
-- FlowMusic（歌曲/音乐）Google 账号池。
--
-- credential_enc 存「凭证 bundle」JSON（AES-256-GCM 加密）：
--   {"refresh_token":..,"access_token":..,"provider_token":..,
--    "provider_refresh_token":..,"flow_bearer":..,"cookies":..}
-- 其中 access_token 是 Supabase JWT —— FlowMusic 业务接口真正校验的 Bearer。
-- 续期调度器解密整包 → RefreshSupabase → 回写整包。
--
CREATE TABLE IF NOT EXISTS `pool_google` (
  `id`                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `email`               VARCHAR(255) NOT NULL,
  `display_name`        VARCHAR(128) DEFAULT NULL,
  `credential_enc`      BLOB         NOT NULL                              COMMENT 'AES-256-GCM 加密的凭证 bundle JSON',
  `protocol_mode`       VARCHAR(32)  NOT NULL DEFAULT 'refresh_token'      COMMENT 'refresh_token / bearer / protocol',
  `status`              VARCHAR(32)  NOT NULL DEFAULT 'valid'              COMMENT 'valid / invalid / disabled / cooldown',
  `source`              VARCHAR(32)  NOT NULL DEFAULT 'import'             COMMENT 'import / register',
  `credits`             DECIMAL(12,2) NOT NULL DEFAULT 0                   COMMENT 'FlowMusic 剩余积分',
  `tokens_remaining`    DECIMAL(14,2) NOT NULL DEFAULT 0                   COMMENT 'FlowMusic 剩余 tokens',
  `subscription_tier`   VARCHAR(64)  DEFAULT NULL,
  `proxy_id`            BIGINT UNSIGNED DEFAULT NULL,
  `expires_at`          DATETIME(3)  DEFAULT NULL                          COMMENT 'access_token(Supabase JWT) 失效时间',
  `last_checked_at`     DATETIME(3)  DEFAULT NULL,
  `last_refresh_at`     DATETIME(3)  DEFAULT NULL,
  `last_refresh_result` VARCHAR(255) DEFAULT NULL,
  `last_used_at`        DATETIME(3)  DEFAULT NULL,
  `refresh_enabled`     TINYINT      NOT NULL DEFAULT 1                    COMMENT '0关闭 1启用自动刷新',
  `failure_count`       INT          NOT NULL DEFAULT 0,
  `error_message`       VARCHAR(500) DEFAULT NULL,
  `cooldown_until`      DATETIME(3)  DEFAULT NULL,
  `notes`               VARCHAR(500) DEFAULT NULL,
  `created_at`          DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at`          DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at`          DATETIME(3)  DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_pool_google_email` (`email`),
  KEY `idx_pool_google_status` (`status`),
  KEY `idx_pool_google_expires` (`expires_at`),
  KEY `idx_pool_google_deleted` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='FlowMusic 歌曲 Google 号池';
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS `pool_google`;
