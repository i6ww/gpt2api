-- +goose Up
-- +goose StatementBegin

-- ──────────────────────────────────────────────────────────────────────────
-- Plus 升级资源池
--
-- 这一组表服务于"GPT 号池 → 批量开 Plus"的场景，主链路：
--   1. dispatcher 锁 GPT 账号 + 取注册时代理 (cs_proxy)
--   2. 锁 gopay_wallet_pool 一行，FOR UPDATE SKIP LOCKED 保证不撞钱包
--   3. 取该钱包绑定的 cloud_phone_pool 一行（后面用 GeeLark API 读 WhatsApp OTP）
--   4. 取 payment_proxy_pool 一行（印尼住宅 IP，专用于 Phase B 的 Midtrans/GoPay 阶段）
--   5. 拉起 gopay.py 子进程跑 15 步 HTTP 流程
--   6. 成功后写 gopay_wallet_binding 一行（追踪可取消订阅的状态）
-- ──────────────────────────────────────────────────────────────────────────

-- 1. GeeLark 云手机池
CREATE TABLE IF NOT EXISTS `cloud_phone_pool` (
  `id`               VARCHAR(64)  NOT NULL                          COMMENT 'GeeLark phone_id（OpenAPI 主键）',
  `name`             VARCHAR(128) NOT NULL DEFAULT ''               COMMENT '人类可读名称（备注用）',
  `gl_token_enc`     VARBINARY(512) NOT NULL                        COMMENT 'AES-256-GCM 加密的 GeeLark Bearer Token',
  `adb_addr`         VARCHAR(128) DEFAULT NULL                      COMMENT '可选 ADB 地址 IP:PORT:PWD（有值=本机能直连，速度更快）',
  `prefer_api`       TINYINT(1)   NOT NULL DEFAULT 1                COMMENT '1=API 模式（默认，服务器跑必选）/ 0=ADB 模式',
  `bound_phone`      VARCHAR(32)  DEFAULT NULL                      COMMENT '反查：这台机里 WhatsApp 绑定的手机号（E.164 不带 +）',
  `status`           VARCHAR(16)  NOT NULL DEFAULT 'online'         COMMENT 'online / offline / banned / disabled',
  `last_check_at`    DATETIME(3)  DEFAULT NULL,
  `last_check_ok`    TINYINT(1)   NOT NULL DEFAULT 0                COMMENT '0=未知 / 1=ok / 2=fail',
  `last_error`       VARCHAR(255) DEFAULT NULL,
  `remark`           VARCHAR(255) DEFAULT NULL,
  `created_by`       BIGINT UNSIGNED DEFAULT NULL,
  `created_at`       DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at`       DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at`       DATETIME(3)  DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_status` (`status`),
  KEY `idx_bound_phone` (`bound_phone`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='GeeLark 云手机池（用于读 WhatsApp OTP）';


-- 2. GoPay 钱包池
--
-- "一钱包重复开 N 个 Plus" 模型：
--   active_plus_count 记录当前正绑定的活跃 Plus 数；满 per_wallet_quota
--   后置 status='exhausted'，等其中一个 binding 取消订阅后再 -1 转回 available。
CREATE TABLE IF NOT EXISTS `gopay_wallet_pool` (
  `id`                 BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `country_code`       VARCHAR(8)   NOT NULL                        COMMENT '国家代码不带 +（印尼=62）',
  `phone_number`       VARCHAR(32)  NOT NULL                        COMMENT '手机号 E.164 不带 + 不带国家码（GoPay linking 用）',
  `pin_enc`            VARBINARY(256) NOT NULL                      COMMENT 'AES-256-GCM 加密的 GoPay 6 位 PIN',
  `cloud_phone_id`     VARCHAR(64)  NOT NULL                        COMMENT '关联 cloud_phone_pool.id（接 WhatsApp OTP）',
  `status`             VARCHAR(16)  NOT NULL DEFAULT 'available'    COMMENT 'available / leased / cooldown / banned / exhausted / disabled',
  `active_plus_count`  INT          NOT NULL DEFAULT 0              COMMENT '当前正绑定的活跃 Plus 数（升级 +1，取消订阅 -1）',
  `total_success`      INT          NOT NULL DEFAULT 0,
  `total_failed`       INT          NOT NULL DEFAULT 0,
  `last_used_at`       DATETIME(3)  DEFAULT NULL,
  `last_error`         VARCHAR(255) DEFAULT NULL,
  `cooldown_until`     DATETIME(3)  DEFAULT NULL                    COMMENT '失败/风控触发的冷却截止时间，到点自动转回 available',
  `remark`             VARCHAR(255) DEFAULT NULL,
  `created_by`         BIGINT UNSIGNED DEFAULT NULL,
  `created_at`         DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at`         DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at`         DATETIME(3)  DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_phone` (`country_code`, `phone_number`),
  KEY `idx_status_cooldown` (`status`, `cooldown_until`),
  KEY `idx_cloud_phone` (`cloud_phone_id`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='GoPay 钱包池（一钱包多 Plus）';


-- 3. 钱包-账号绑定
--
-- 每次成功开 Plus 都写一行；取消订阅时 status='cancelled' 并 active_plus_count - 1。
-- 用于追踪：哪个钱包还能开几个 / 某个 Plus 是哪个钱包付的款 / 30 天到期前是否需要续费等。
CREATE TABLE IF NOT EXISTS `gopay_wallet_binding` (
  `id`             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `wallet_id`      BIGINT UNSIGNED NOT NULL                         COMMENT '关联 gopay_wallet_pool.id',
  `gpt_account_id` BIGINT UNSIGNED NOT NULL                         COMMENT '关联 pool_gpt.id',
  `cs_id`          VARCHAR(128) DEFAULT NULL                        COMMENT 'Stripe checkout session id (cs_live_xxx)',
  `charge_ref`     VARCHAR(64)  DEFAULT NULL                        COMMENT 'Midtrans charge_ref (Axxxxx)，取消订阅时反查',
  `amount_idr`     BIGINT       NOT NULL DEFAULT 0                  COMMENT '本次扣款金额（IDR cents）',
  `charged_at`     DATETIME(3)  NOT NULL                            COMMENT '扣款成功时间',
  `expires_at`     DATETIME(3)  NOT NULL                            COMMENT '订阅到期时间（默认 +30d）',
  `status`         VARCHAR(16)  NOT NULL DEFAULT 'active'           COMMENT 'active / cancelled / expired / refunded',
  `cancelled_at`   DATETIME(3)  DEFAULT NULL,
  `note`           VARCHAR(255) DEFAULT NULL,
  `created_at`     DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at`     DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_wallet_status` (`wallet_id`, `status`),
  KEY `idx_account` (`gpt_account_id`),
  KEY `idx_expires` (`expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='GoPay 钱包-Plus 账号绑定（取消订阅追踪）';


-- 4. 印尼支付代理池
--
-- 跟 proxy 表（号注册代理）独立，因为：
--   - Phase B 的 Midtrans/GoPay 必须用印尼/东南亚 IP，否则 OTP 风控
--   - 大部分注册代理是 US / 全球 IP，这里要的是住宅 IP 池，规模/成本/质量需求都不一样
CREATE TABLE IF NOT EXISTS `payment_proxy_pool` (
  `id`             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `name`           VARCHAR(128) NOT NULL DEFAULT ''                 COMMENT '人类可读名称',
  `scheme`         VARCHAR(8)   NOT NULL DEFAULT 'http'             COMMENT 'http / https / socks5 / socks5h',
  `host`           VARCHAR(255) DEFAULT NULL                        COMMENT '静态代理：直接 host:port；动态代理：留空，用 api_url',
  `port`           INT          NOT NULL DEFAULT 0,
  `username`       VARCHAR(128) DEFAULT NULL,
  `password_enc`   VARBINARY(256) DEFAULT NULL                      COMMENT 'AES-256-GCM 加密的密码',
  `api_url`        VARCHAR(512) DEFAULT NULL                        COMMENT '动态拨号 API：每次取代理就 GET 这个 URL，返回一行 host:port:user:pass 或 user:pass@host:port',
  `country`        VARCHAR(8)   NOT NULL DEFAULT 'ID'               COMMENT 'ISO 国家码，默认 ID（印尼）',
  `status`         VARCHAR(16)  NOT NULL DEFAULT 'active'           COMMENT 'active / disabled / banned',
  `total_used`     INT          NOT NULL DEFAULT 0,
  `total_failed`   INT          NOT NULL DEFAULT 0,
  `last_used_at`   DATETIME(3)  DEFAULT NULL,
  `last_check_at`  DATETIME(3)  DEFAULT NULL,
  `last_check_ok`  TINYINT(1)   NOT NULL DEFAULT 0,
  `last_check_ms`  INT          NOT NULL DEFAULT 0,
  `last_error`     VARCHAR(255) DEFAULT NULL,
  `remark`         VARCHAR(255) DEFAULT NULL,
  `created_by`     BIGINT UNSIGNED DEFAULT NULL,
  `created_at`     DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at`     DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at`     DATETIME(3)  DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_status` (`status`),
  KEY `idx_country` (`country`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='印尼支付代理池（GoPay Phase B 专用）';


-- ──────────────────────────────────────────────────────────────────────────
-- system_config 默认 KV：plus_upgrade.* / geelark.*
-- ──────────────────────────────────────────────────────────────────────────
INSERT INTO `system_config` (`key`, `value`, `remark`) VALUES
  ('plus_upgrade.enabled',                   'true',                                              '是否启用 GPT 批量开 Plus 功能'),
  ('plus_upgrade.python_path',               '"/usr/local/bin/python3"',                          'gopay.py 解释器路径'),
  ('plus_upgrade.gopay_script_path',         '"/app/scripts/gopay.py"',                           '改造后的 gopay.py 脚本路径（容器内）'),
  ('plus_upgrade.task_concurrency',          '8',                                                 'Plus 升级任务全局并发数 [1, 64]'),
  ('plus_upgrade.per_wallet_quota',          '30',                                                '一个 GoPay 钱包最多能绑定的活跃 Plus 数 [1, 100]'),
  ('plus_upgrade.wallet_cooldown_min',       '60',                                                'GoPay 钱包失败/风控后的冷却分钟 [1, 1440]'),
  ('plus_upgrade.otp_poll_interval_s',       '2',                                                 '云手机 OTP 拉取轮询间隔秒 [1, 30]'),
  ('plus_upgrade.otp_timeout_s',             '180',                                               '云手机 OTP 等待超时秒 [30, 600]'),
  ('plus_upgrade.cs_proxy_strategy',         '"account_proxy"',                                   'Phase A 代理策略：account_proxy=用账号注册代理 / payment_pool=用印尼支付池'),
  ('plus_upgrade.ext_proxy_strategy',        '"payment_pool"',                                    'Phase B 代理策略：payment_pool=用印尼支付池（推荐）/ account_proxy=用账号代理'),
  ('geelark.api_base',                       '"https://openapi.geelark.cn/open/v1"',              'GeeLark OpenAPI 基础地址')
ON DUPLICATE KEY UPDATE `key`=`key`;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS `gopay_wallet_binding`;
DROP TABLE IF EXISTS `gopay_wallet_pool`;
DROP TABLE IF EXISTS `payment_proxy_pool`;
DROP TABLE IF EXISTS `cloud_phone_pool`;

DELETE FROM `system_config` WHERE `key` IN (
  'plus_upgrade.enabled',
  'plus_upgrade.python_path',
  'plus_upgrade.gopay_script_path',
  'plus_upgrade.task_concurrency',
  'plus_upgrade.per_wallet_quota',
  'plus_upgrade.wallet_cooldown_min',
  'plus_upgrade.otp_poll_interval_s',
  'plus_upgrade.otp_timeout_s',
  'plus_upgrade.cs_proxy_strategy',
  'plus_upgrade.ext_proxy_strategy',
  'geelark.api_base'
);

-- +goose StatementEnd
