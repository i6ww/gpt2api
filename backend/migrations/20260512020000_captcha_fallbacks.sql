-- +goose Up
-- +goose StatementBegin

-- captcha.arkose.fallbacks / captcha.turnstile.fallbacks: 主家失败时按顺序 fallback 的备用打码服务列表。
--
-- 数据形态: JSON 数组 [{ "provider": "nopecha", "api_key": "NP-...", "endpoint": "" }, ...]
--   - provider 大小写不敏感（后端 ToLower）
--   - api_key 必填，空项自动丢弃
--   - endpoint 留空时按 provider 走默认 endpoint
--   - 留空 [] 表示无备用，行为完全等同旧版单家模式
--
-- 经验值（Adobe Arkose 公钥解题率）:
--   - 单家 anti-captcha:    ~70-85%
--   - + nopecha fallback:    ~91%（1 - (1-0.78)^2）
--   - + nopecha + yescaptcha: ~97%
--
-- per-attempt 超时按链长度自适应: 1 家 60s，2 家 45s/家，3+ 家 35s/家。
INSERT INTO `system_config` (`key`, `value`, `remark`) VALUES
  ('captcha.arkose.fallbacks',    '[]', 'Arkose 备用服务商 JSON 数组；主家超时/UNSOLVABLE 立刻 fail-over 到下家，不消耗邮箱'),
  ('captcha.turnstile.fallbacks', '[]', 'Turnstile 备用服务商 JSON 数组；语义同 arkose.fallbacks')
ON DUPLICATE KEY UPDATE `key`=`key`;

-- +goose StatementEnd

-- +goose Down
DELETE FROM `system_config` WHERE `key` IN
  ('captcha.arkose.fallbacks', 'captcha.turnstile.fallbacks');
