-- +goose Up
-- +goose StatementBegin

-- 号池注册任务日志的 message 字段升级到 TEXT。
-- VARCHAR(1000) 在 Grok RSC / Cloudflare HTML 错误页这种长 body 下太短，
-- 拍扁后无法回放完整失败上下文。升级到 TEXT（最大 64KB）。
ALTER TABLE `register_task_log`
  MODIFY COLUMN `message` TEXT NULL COMMENT '日志正文（升级到 TEXT 以容纳长响应）';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE `register_task_log`
  MODIFY COLUMN `message` VARCHAR(1000) DEFAULT NULL COMMENT '日志正文';
-- +goose StatementEnd
