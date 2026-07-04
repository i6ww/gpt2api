-- pool_grok：补齐扫描续期需要的运维字段
--
-- 与 pool_adobe 风格一致：
--   failure_count   - 连续刷新失败次数（达阈值 → 置 trial_status=failed/expired）
--   last_checked_at - 最近一次刷新时间（前端刷新按钮 / 后台调度都打）
--   quota_total     - 当前账号订阅"窗口总配额"
--                     X.com /rest/rate-limits 返回的 totalQueries（auto 模式）
--                     用于推断 account_type：
--                       150 → super_grok_heavy
--                        50 → super_grok
--                        20 → free
--                     存下来给 UI 显示 X/Y 形式的 “credits/quota_total”
ALTER TABLE `pool_grok`
  ADD COLUMN `failure_count`   INT          NOT NULL DEFAULT 0 AFTER `credits`,
  ADD COLUMN `last_checked_at` DATETIME(3)  DEFAULT NULL       AFTER `failure_count`,
  ADD COLUMN `quota_total`     DECIMAL(12, 2) NOT NULL DEFAULT 0 AFTER `last_checked_at`;
