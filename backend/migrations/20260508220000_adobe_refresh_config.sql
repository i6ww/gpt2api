-- Adobe 号池后台续期 / 积分刷新调度器配置。
--
-- 写入 system_config 一条 JSON：
--
--   key = adobe.refresh
--   value = {
--     "enabled": true,                       -- 总开关
--     "threshold_hours": 12,                 -- access_token 距过期 < N 小时 → 触发 silent refresh
--     "scan_interval_sec": 60,               -- 后台扫描周期（秒）
--     "credits_recheck_minutes": 30,         -- 积分缓存有效期（分钟），过期后用现有 token 拉新积分
--     "max_concurrent": 4                    -- 同时刷新多少个账号（避免打满 firefly / IMS 限流）
--   }
--
-- 默认值与 Python 参考实现 (newwork/token_refresh.py) 一致；上线后可在
-- 「系统配置」页面随时调整，不需要重启服务。
INSERT INTO `system_config` (`key`, `value`, `remark`)
VALUES (
  'adobe.refresh',
  JSON_OBJECT(
    'enabled', TRUE,
    'threshold_hours', 12,
    'scan_interval_sec', 60,
    'credits_recheck_minutes', 30,
    'max_concurrent', 4
  ),
  'Adobe 号池：< N 小时即将过期的 access_token 自动 silent refresh，并定期更新 Firefly 积分'
)
ON DUPLICATE KEY UPDATE
  `value`  = VALUES(`value`),
  `remark` = VALUES(`remark`);
