-- pool_grok 增补 subscription status + stripe product id。
--
-- 让号池能区分 "三天试用 (trialing) / 正式订阅 (active) / 欠费 (past_due)"
-- 以及 stripe 层 product 精确档位 (Lite / SuperGrok / Heavy)。
--
-- 历史 bug：早期版本写成了 sql-migrate 的 `+migrate Up/Down`，被 mysql-init
-- 当裸 SQL 全量执行（Up 加列后 Down 又 DROP），导致列从来没建出来。
-- 这里统一改成 goose 风格。
-- +goose Up
-- +goose StatementBegin
ALTER TABLE `pool_grok`
  ADD COLUMN `subscription_status` VARCHAR(32) NOT NULL DEFAULT ''
    COMMENT '订阅生命周期：active/trialing/past_due/canceled/inactive' AFTER `billing_interval`,
  ADD COLUMN `product_id` VARCHAR(64) NOT NULL DEFAULT ''
    COMMENT 'stripe productId, 精确识别 Lite/SuperGrok/Heavy' AFTER `subscription_status`;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE `pool_grok`
  DROP COLUMN `subscription_status`,
  DROP COLUMN `product_id`;
-- +goose StatementEnd
