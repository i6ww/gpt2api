-- pool_grok 增加两列：账号订阅类型 + 剩余额度
--
--   account_type  - X.com / SuperGrok 账号类型
--                   '' / 'free' / 'super_grok' / 'super_grok_heavy' / 'team' / 'unknown'
--                   预留 32 字节足够未来扩展
--   credits       - 当日/当周剩余额度（条数或积分）
--                   X.com Free=20/2h，SuperGrok=100/2h，Heavy 不限
--                   后续由刷新任务回填，当前先存“最近一次回填快照”
--                   DECIMAL(12,2) 与 Adobe 保持一致，便于前端复用渲染逻辑
ALTER TABLE `pool_grok`
  ADD COLUMN `account_type` VARCHAR(32) NOT NULL DEFAULT '' AFTER `trial_error`,
  ADD COLUMN `credits`      DECIMAL(12, 2) NOT NULL DEFAULT 0 AFTER `account_type`;

ALTER TABLE `pool_grok`
  ADD KEY `idx_pool_grok_account_type` (`account_type`);
