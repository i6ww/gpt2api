-- +goose Up
-- +goose StatementBegin
-- 2026-05-13 上游 API 管理 Phase A + B
-- 三张新表：
--   upstream_channel       一个「通道」= provider + route + billing_mode + 计价单位
--   upstream_model_route   内部 model_code (+ variant_key) → upstream_channel 多对多
--   task_cost_log          每次成功调用一条；记录 cost_micro_usd & sale_points，
--                          后续利润报表全靠它聚合
--
-- 设计要点：
--   1. cost 用 micro_usd（USD * 1e6）整数存。汇率走 fx_usd_to_cny snapshot
--      记录到每行 log 上，历史报表不被未来汇率漂移污染。
--   2. unit_price 用 JSON：billing_mode 不同字段不同，避免每改一种计费模式
--      就改一次表结构（per_call 用 {micro_usd}, per_token_io 用 {input_per_1k, output_per_1k}…）
--   3. capabilities JSON 标明这个通道能做什么 kind/variant，admin 后台 UI
--      用来过滤可选项。
--   4. task_cost_log 用 ref_type+ref_id（generation 任务用 task_id；chat 也用同样字段；
--      register 任务后续 Phase E 复用同一张表存"获客成本"），所以 ref_id 用 VARCHAR(64)
--      而不是绑死 char(26)。
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS `upstream_channel` (
  `id`                     BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `key`                    VARCHAR(64)     NOT NULL                        COMMENT '稳定 key，如 adobe.firefly.image_2k；admin UI 显示用',
  `provider`               VARCHAR(32)     NOT NULL                        COMMENT 'gpt / grok / adobe / pic2api / xxapi 等',
  `route`                  VARCHAR(48)     NOT NULL DEFAULT ''             COMMENT 'web / api / oauth / firefly / imagine 等子路径',
  `base_url`               VARCHAR(255)    NOT NULL DEFAULT ''             COMMENT '通道默认 base，可被 account.base_url 覆盖',
  `label`                  VARCHAR(120)    NOT NULL DEFAULT ''             COMMENT '运营给的中文别名，仅显示',
  `enabled`                TINYINT(1)      NOT NULL DEFAULT 1,
  `billing_mode`           VARCHAR(32)     NOT NULL DEFAULT 'per_call'     COMMENT 'per_call / per_unit / per_token_io / per_credit / subscription / custom',
  `unit_price`             JSON            NOT NULL                        COMMENT '随 billing_mode 变化的价格字段；详见 service.CostRecorder',
  `currency`               CHAR(3)         NOT NULL DEFAULT 'USD',
  `capabilities`           JSON            NOT NULL                        COMMENT '{kinds:[image|video|chat|register], variants:[1k,2k,4k,…]}',
  `monthly_fixed_cost`     BIGINT          NOT NULL DEFAULT 0              COMMENT '订阅类通道：月费(micro_usd)。per_call 通道留 0',
  `expected_monthly_calls` BIGINT          NOT NULL DEFAULT 0              COMMENT '订阅类通道用于摊销的预计月调用次数',
  `fx_to_cny`              DECIMAL(10,4)   NOT NULL DEFAULT 0.0000         COMMENT '通道本币 → CNY 兜底汇率；优先使用 system_config.fx.*',
  `notes`                  TEXT            NULL,
  `created_at`             DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at`             DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_upstream_channel_key` (`key`),
  KEY `idx_provider_route` (`provider`, `route`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='上游通道：一个 provider + 路径 + 计价模式';
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS `upstream_model_route` (
  `id`                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `model_code`          VARCHAR(64)     NOT NULL                        COMMENT '内部 model_code，对齐 billing.model_prices',
  `variant_key`         VARCHAR(32)     NOT NULL DEFAULT ''             COMMENT '可选分档：image:1k/2k/4k、video:6/10/20/30、chat 留空',
  `upstream_channel_id` BIGINT UNSIGNED NOT NULL,
  `priority`            SMALLINT        NOT NULL DEFAULT 1              COMMENT '同 model+variant 内 priority ASC 优先；默认 1',
  `enabled`             TINYINT(1)      NOT NULL DEFAULT 1,
  `cost_multiplier`     DECIMAL(6,3)    NOT NULL DEFAULT 1.000          COMMENT '同通道下针对此路由的乘数（如 4K = 4×）',
  `notes`               VARCHAR(255)    NULL,
  `created_at`          DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at`          DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_model_variant_priority` (`model_code`, `variant_key`, `priority`),
  KEY `idx_channel` (`upstream_channel_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='内部模型/分档 → 上游通道路由表';
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS `task_cost_log` (
  `id`                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `ref_type`            VARCHAR(16)     NOT NULL                        COMMENT 'generation / chat / register / acquisition',
  `ref_id`              VARCHAR(64)     NOT NULL                        COMMENT 'generation/chat → task_id；register → register_task.id；其它见调用方约定',
  `user_id`             BIGINT UNSIGNED NULL                            COMMENT '业务侧用户 id，用户级利润看这个',
  `upstream_channel_id` BIGINT UNSIGNED NOT NULL,
  `account_id`          BIGINT UNSIGNED NULL                            COMMENT '上游账号（号池 id），用于按号查成本',
  `model_code`          VARCHAR(64)     NULL,
  `variant_key`         VARCHAR(32)     NULL,
  `unit_label`          VARCHAR(32)     NULL                            COMMENT 'call / image / credit / 1k_token_in 等',
  `unit_qty`            DECIMAL(14,4)   NOT NULL DEFAULT 1.0000,
  `cost_micro_usd`      BIGINT          NOT NULL DEFAULT 0              COMMENT '此次调用上游开销(USD * 1e6)；可为负表示退款',
  `sale_points`         BIGINT          NOT NULL DEFAULT 0              COMMENT '同步快照 task.cost_points（点 *100）',
  `sale_micro_cny`      BIGINT          NOT NULL DEFAULT 0              COMMENT '把销售点数转人民币的快照值；后续报表跟 cost_micro_usd 算差额',
  `fx_usd_to_cny`       DECIMAL(10,4)   NOT NULL DEFAULT 0.0000         COMMENT '记录时刻 USD→CNY 汇率',
  `recorded_at`         DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_task_cost_ref` (`ref_type`, `ref_id`),
  KEY `idx_task_cost_recorded` (`recorded_at`),
  KEY `idx_task_cost_channel_recorded` (`upstream_channel_id`, `recorded_at`),
  KEY `idx_task_cost_model_recorded` (`model_code`, `recorded_at`),
  KEY `idx_task_cost_user_recorded` (`user_id`, `recorded_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='每次上游调用一行；利润报表的事实表';
-- +goose StatementEnd

-- +goose StatementBegin
-- system_config 种子：汇率 + 是否启用 cost 记录（默认 true）。
-- 这两个 key 在 service.SystemConfigService 里也有对应常量。
INSERT INTO `system_config` (`key`, `value`, `updated_by`)
VALUES
  ('fx.usd_to_cny',          '7.2',                           NULL),
  ('fx.idr_to_cny',          '0.00046',                       NULL),
  ('cost_log.enabled',       'true',                          NULL),
  ('cost_log.point_to_cny',  '0.01',                          NULL)
ON DUPLICATE KEY UPDATE `key` = `key`;
-- 1 个销售点 = 0.01 CNY，对应 1 点 ≈ 1 分。
-- 与 billing 表 points*100 阶梯保持一致（unit_points=100 = 1 点 = 1 分）。
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS `task_cost_log`;
DROP TABLE IF EXISTS `upstream_model_route`;
DROP TABLE IF EXISTS `upstream_channel`;
-- 不回滚 system_config 种子；JSON KV 安全留着不影响其它逻辑。
-- +goose StatementEnd
