-- +goose Up
-- +goose StatementBegin
--
-- 给 pool_adobe 增加 entitlements_json 字段。
--
-- 背景：Adobe Firefly 的高档位（典型 4K Premium 出图）需要账号本身买了对应权益，
--       Free 号在 1K/2K 完全可用，但调 4K 端点会返回 403 + x-access-error=user_not_entitled。
--       这个错误"不是 token 坏了"，所以不能熔断/扣 error_count，但每次跑 4K
--       任务还撞到这个号是非常浪费 retry 次数的。
--
--       这一列让 generation_service 学到"这个号在哪个档位上 not entitled"，
--       下次相同档位的任务直接跳过该号；7 天后标记自动失效，允许重新探测
--       （运营可能给老号充值升级了 Premium）。
--
-- 结构：
--   {"no_4k": true, "no_4k_checked_at": 1731331200, "no_2k": false, ...}
--
-- 字段全可空：NULL = 该号从未撞过 entitlement 错误，按"全档位都行"对待。
--

ALTER TABLE `pool_adobe`
  ADD COLUMN `entitlements_json` JSON DEFAULT NULL
    COMMENT 'JSON: 学到的档位权益状态，例 {"no_4k": true, "no_4k_checked_at": 1731331200}; NULL = 未撞过 not_entitled'
    AFTER `error_message`;

-- +goose StatementEnd

-- +goose Down
ALTER TABLE `pool_adobe`
  DROP COLUMN `entitlements_json`;
