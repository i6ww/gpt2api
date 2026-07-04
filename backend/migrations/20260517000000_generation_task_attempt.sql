-- +goose Up
-- +goose StatementBegin
-- 2026-05-17 给 generation_task 加 attempt 计数器
--
-- 背景：远端 agent 节点的失败回报（ApplyAgentResult Failed 分支）原来是直接
-- SetFailed + FailRefund，等于一次失败终结，不会像 inline runTask 那样
-- 在 maxAttempts 内换号 / 换节点重试。
--
-- 修复后的语义：
--   - ClaimBatch 每抢锁一次任务 → attempt += 1（embedded 与远端 agent 都计入）
--   - ApplyAgentResult 失败分支：当错误判定为 retryableProviderError，
--     且 attempt < cfg.retry_max_attempts → ReleaseClaim 让任务回 pending，
--     由下一轮 lease 再换号重试；否则真正 SetFailed + FailRefund。
--   - ReclaimExpired（lease 过期回收）不会清掉 attempt，避免节点反复挂掉
--     却让任务以为没出过任何问题。
--
-- TINYINT 上限 255 远高于实际 retry_max_attempts（默认 3），不会溢出；
-- 服务侧使用 int8 即可。
ALTER TABLE `generation_task`
    ADD COLUMN `attempt` TINYINT UNSIGNED NOT NULL DEFAULT 0
    COMMENT '集群 lease 累计次数，超过 cfg.retry_max_attempts 后真失败';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE `generation_task` DROP COLUMN `attempt`;
-- +goose StatementEnd
