-- +goose Up
-- +goose StatementBegin
-- 邀请返佣：
--   被邀请用户每发生一次 "充值"（wallet_log biz_type=recharge / direction=1），
--   系统按 invite.commission_rate_bp（基点，10000=100%；默认 1000=10%）
--   给其邀请人计入返佣点数（biz_type=invite_reward）。
--
-- invite_reward_log 用作 (source_log_id) 上的唯一约束实现幂等：
--   - source_log_id 指向触发本次返佣的充值 wallet_log.id
--   - 同一条充值流水只能产生一笔返佣
--   - wallet_log_id 指向真正入账给邀请人的 wallet_log.id（biz_type=invite_reward）
CREATE TABLE IF NOT EXISTS `invite_reward_log` (
  `id`              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `inviter_id`      BIGINT UNSIGNED NOT NULL COMMENT '获得返佣的用户',
  `invitee_id`      BIGINT UNSIGNED NOT NULL COMMENT '被邀请的用户（充值发生者）',
  `source_log_id`   BIGINT UNSIGNED NOT NULL COMMENT '来源 wallet_log.id（充值流水）',
  `recharge_points` BIGINT NOT NULL COMMENT '被邀请者本次充值积分（与 wallet_log.points 同口径）',
  `reward_points`   BIGINT NOT NULL COMMENT '本次返佣给邀请人的积分',
  `rate_bp`         INT NOT NULL COMMENT '本次返佣比例（基点，10000=100%）',
  `wallet_log_id`   BIGINT UNSIGNED NOT NULL COMMENT '邀请人入账 wallet_log.id（biz_type=invite_reward）',
  `created_at`      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_source` (`source_log_id`),
  KEY `idx_inviter_created` (`inviter_id`, `created_at`),
  KEY `idx_invitee` (`invitee_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='邀请返佣明细';

-- 默认 10% 返佣率（10000 基点 = 100%）
INSERT INTO `system_config` (`key`, `value`, `remark`)
VALUES (
  'invite.commission_rate_bp',
  '1000',
  '邀请返佣比例（基点，10000=100%；默认 1000=10%）'
)
ON DUPLICATE KEY UPDATE `key` = `key`;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM `system_config` WHERE `key` = 'invite.commission_rate_bp';
DROP TABLE IF EXISTS `invite_reward_log`;
-- +goose StatementEnd
