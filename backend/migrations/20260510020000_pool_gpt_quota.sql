-- +goose Up
-- +goose StatementBegin

-- pool_gpt 加额度 / 账号类型 / 配额检查时间字段。
-- 来源：ChatGPT backend `wham/usage` endpoint + 解 access_token JWT。
ALTER TABLE `pool_gpt`
  ADD COLUMN `plan_type`                       VARCHAR(32)  DEFAULT NULL COMMENT 'free / plus / pro / team / enterprise / unknown' AFTER `oauth_client_id`,
  ADD COLUMN `chatgpt_account_id`              VARCHAR(64)  DEFAULT NULL COMMENT 'wham/usage.account_id（OpenAI 账号 UUID）'      AFTER `plan_type`,
  ADD COLUMN `quota_primary_used_percent`      DECIMAL(5,2) DEFAULT NULL COMMENT '短窗口（5h）已用百分比 0~100'                  AFTER `chatgpt_account_id`,
  ADD COLUMN `quota_primary_reset_at`          DATETIME(3)  DEFAULT NULL COMMENT '短窗口下次重置时间'                              AFTER `quota_primary_used_percent`,
  ADD COLUMN `quota_secondary_used_percent`    DECIMAL(5,2) DEFAULT NULL COMMENT '长窗口（7d）已用百分比 0~100'                   AFTER `quota_primary_reset_at`,
  ADD COLUMN `quota_secondary_reset_at`        DATETIME(3)  DEFAULT NULL COMMENT '长窗口下次重置时间'                              AFTER `quota_secondary_used_percent`,
  ADD COLUMN `quota_code_review_used_percent`  DECIMAL(5,2) DEFAULT NULL COMMENT 'code review 短窗口已用百分比'                   AFTER `quota_secondary_reset_at`,
  ADD COLUMN `last_quota_check_at`             DATETIME(3)  DEFAULT NULL COMMENT '上次 wham/usage 探测时间'                       AFTER `quota_code_review_used_percent`;

-- 默认 plan_type=unknown 让首次扫描前 UI 不会出现 NULL。
UPDATE `pool_gpt`
   SET `plan_type` = 'unknown'
 WHERE `plan_type` IS NULL;

-- 默认 GPT 自动续期配置写入 system_config（与 adobe.refresh 对齐结构）。
-- 历史 bug：早期版本误写了不存在的 description 字段，会导致 mysql 容器
-- 初始化时整个迁移链路中断；这里改回真正的列名 remark。
INSERT INTO `system_config` (`key`, `value`, `remark`)
VALUES (
  'gpt.refresh',
  '{"enabled":true,"threshold_hours":12,"scan_interval_sec":120,"quota_recheck_minutes":30,"max_concurrent":3}',
  'GPT 号池自动续期 / 额度刷新配置'
)
ON DUPLICATE KEY UPDATE `remark` = VALUES(`remark`);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE `pool_gpt`
  DROP COLUMN `last_quota_check_at`,
  DROP COLUMN `quota_code_review_used_percent`,
  DROP COLUMN `quota_secondary_reset_at`,
  DROP COLUMN `quota_secondary_used_percent`,
  DROP COLUMN `quota_primary_reset_at`,
  DROP COLUMN `quota_primary_used_percent`,
  DROP COLUMN `chatgpt_account_id`,
  DROP COLUMN `plan_type`;

DELETE FROM `system_config` WHERE `key` = 'gpt.refresh';

-- +goose StatementEnd
