-- pool_grok 增补 subscription metadata。
--
-- expires_at 实际语义跟 subscription 状态强相关：
--   - cancel_at_period_end=false → 这是下次"自动续费"扣费日，过了会延一个 interval
--   - cancel_at_period_end=true  → 这是真正的"到期失效"日
-- 同时存下 billing_interval（monthly / yearly）方便 UI 显示 + 后端 stale 数据
-- 自动外推。
--
-- 历史 bug：早期版本写成了 sql-migrate 的 `+migrate Up/Down`，但项目其它
-- 迁移以及 mysql-init 的 awk 解析器都按 goose 风格（`+goose Up/Down` +
-- StatementBegin/End）处理，结果 Up/Down 段同时被执行，列加了又被 drop，
-- 后续迁移引用 billing_interval 报 Unknown column。统一改成 goose 风格。
-- +goose Up
-- +goose StatementBegin
ALTER TABLE `pool_grok`
  ADD COLUMN `cancel_at_period_end` TINYINT(1) NOT NULL DEFAULT 0
    COMMENT '用户已点退订、仍在 period 内可用' AFTER `expires_at`,
  ADD COLUMN `billing_interval` VARCHAR(16) NOT NULL DEFAULT ''
    COMMENT '订阅周期：monthly / yearly / 空' AFTER `cancel_at_period_end`;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE `pool_grok`
  DROP COLUMN `cancel_at_period_end`,
  DROP COLUMN `billing_interval`;
-- +goose StatementEnd
