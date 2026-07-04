-- +goose Up
-- +goose StatementBegin

CREATE TABLE IF NOT EXISTS `phone_pool` (
  `id`              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `provider`        VARCHAR(32)  NOT NULL DEFAULT 'herosms'        COMMENT '接码商：herosms / smstome / ...',
  `service`         VARCHAR(32)  NOT NULL DEFAULT 'dr'             COMMENT '接码商内部服务代号（OpenAI 通常是 dr）',
  `phone`           VARCHAR(32)  NOT NULL                          COMMENT '手机号 E.164 不带 +',
  `country`         INT          NOT NULL DEFAULT 0                COMMENT '接码商国家 ID',
  `activation_id`   VARCHAR(64)  DEFAULT NULL                      COMMENT '当前 activation id（每次 acquire 会更新）',
  `max_uses`        INT          NOT NULL DEFAULT 3                COMMENT '同一手机号最多复用次数（OpenAI 限制 3）',
  `used_count`      INT          NOT NULL DEFAULT 0                COMMENT '已成功用于 OpenAI 注册的次数',
  `failure_count`   INT          NOT NULL DEFAULT 0                COMMENT '失败次数（连续失败超过阈值标 broken）',
  `status`          VARCHAR(32)  NOT NULL DEFAULT 'available'      COMMENT 'available / in_use / exhausted / broken',
  `last_account_id` BIGINT UNSIGNED DEFAULT NULL                   COMMENT '最近一次绑定的 pool_gpt.id',
  `last_used_at`    DATETIME(3)  DEFAULT NULL,
  `last_error`      VARCHAR(255) DEFAULT NULL,
  `created_at`      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at`      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at`      DATETIME(3)  DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_phone` (`phone`),
  KEY `idx_status_used` (`status`, `used_count`),
  KEY `idx_provider` (`provider`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='SMS 接码手机号池';

-- 默认 SMS 配置 keys（值留空，由 admin UI 填写）
INSERT INTO `system_config` (`key`, `value`, `remark`) VALUES
  ('sms.provider',                '"herosms"',                                        '接码商: herosms（暂只支持）'),
  ('sms.api_url',                 '"https://hero-sms.com/stubs/handler_api.php"',     'hero-sms handler API 地址'),
  ('sms.api_key',                 '""',                                               'hero-sms API Key'),
  ('sms.service',                 '"dr"',                                             'hero-sms 服务代号（OpenAI = dr）'),
  ('sms.country',                 '"6"',                                              'hero-sms 国家 ID，逗号分隔多个按顺序尝试。6=印尼 / 25=柬埔寨 / 73=巴西 / 12=美国'),
  ('sms.max_price',               '0.05',                                             '单次 getNumberV2 最高价格 (USD)'),
  ('sms.max_uses',                '3',                                                '一个手机号最多复用次数（OpenAI 限 3）'),
  ('sms.phone_prefix_allowlist',  '"628389"',                                         '号码前缀白名单（E164 不带 +），命中其一才接受。例 "628389" 仅接受 +62 838 9... 段；空=不过滤')
ON DUPLICATE KEY UPDATE `key`=`key`;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS `phone_pool`;

DELETE FROM `system_config` WHERE `key` IN (
  'sms.provider', 'sms.api_url', 'sms.api_key', 'sms.service', 'sms.country',
  'sms.max_price', 'sms.max_uses', 'sms.phone_prefix_allowlist'
);

-- +goose StatementEnd
