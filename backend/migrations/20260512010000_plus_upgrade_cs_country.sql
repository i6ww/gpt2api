-- +goose Up
-- +goose StatementBegin

-- ──────────────────────────────────────────────────────────────────────────
-- Plus 升级 · 修复 CS Proxy（ChatGPT/Stripe 阶段）国家归属
--
-- 背景：上一版 dispatcher 在 Phase A（拿 ChatGPT 支付链接 / Stripe createPM）默认
-- 沿用印尼代理（用于 Phase B 的 GoPay），导致账号注册时的地理位置（如日本）和
-- Phase A 出口位置（印尼）不一致，OpenAI 必然识别为异地登录触发风控。
--
-- 修复策略：
--   - payment_proxy_pool 不变，但 country 字段被赋予新语义：CS=JP/US/etc，Payment=ID
--   - 新增 plus_upgrade.cs_proxy_country：dispatcher 在 Phase A 按此国家从池里抢
--   - 没有匹配代理时，dispatcher 报错（不再静默回退到印尼代理）
-- ──────────────────────────────────────────────────────────────────────────

INSERT INTO `system_config` (`key`, `value`, `remark`) VALUES
  ('plus_upgrade.cs_proxy_country',
   '"JP"',
   'Phase A (ChatGPT/Stripe) 代理国家代号，按 payment_proxy_pool.country 匹配；默认 JP。常用 JP / US / SG。改成空串则禁用 Phase A 专属代理，回退到 Phase B 同款（不推荐）。')
ON DUPLICATE KEY UPDATE `key`=`key`;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM `system_config` WHERE `key` = 'plus_upgrade.cs_proxy_country';
-- +goose StatementEnd
