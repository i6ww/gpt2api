-- +goose Up
-- +goose StatementBegin

-- ──────────────────────────────────────────────────────────────────────────
-- 钱包-云手机字段去冗余
--
-- 背景：一台 GeeLark 云手机 = 一张 SIM = 一个 WhatsApp 号 = 一个 GoPay 钱包，
-- 手机号天然属于云手机。原设计在 gopay_wallet_pool 也存了 country_code +
-- phone_number，导致一对一冗余 + 创建钱包要重复输入手机号。
--
-- 本 migration：
--   1. cloud_phone_pool 加 country_code + phone_number（替代 bound_phone）
--   2. gopay_wallet_pool 删除 country_code + phone_number
--   3. 回填存量数据：先按 wallet.cloud_phone_id 把手机号复制回 cloud_phone_pool
--   4. 索引调整：cloud_phone 新增 (country_code, phone_number)；
--      wallet 删除 uk_phone 唯一索引（一对一约束改由 cloud_phone 承担）
-- ──────────────────────────────────────────────────────────────────────────

-- Step 1: cloud_phone_pool 加新列（先 NULL，便于回填）
ALTER TABLE `cloud_phone_pool`
  ADD COLUMN `country_code` VARCHAR(8)  DEFAULT NULL  COMMENT '国家代码不带 +（默认 62 印尼）' AFTER `bound_phone`,
  ADD COLUMN `phone_number` VARCHAR(32) DEFAULT NULL  COMMENT '手机号 E.164 不带 + 不带国家码（GoPay/WhatsApp 一号一机）' AFTER `country_code`;

-- Step 2: 从 wallet 反向回填到 cloud_phone（同一 cloud_phone_id 可能有多个 wallet，取第一条）
UPDATE `cloud_phone_pool` cp
INNER JOIN (
    SELECT cloud_phone_id, MIN(id) AS wid FROM `gopay_wallet_pool`
    WHERE deleted_at IS NULL AND cloud_phone_id IS NOT NULL AND cloud_phone_id != ''
    GROUP BY cloud_phone_id
) g ON g.cloud_phone_id = cp.id
INNER JOIN `gopay_wallet_pool` w ON w.id = g.wid
SET cp.country_code = w.country_code,
    cp.phone_number = w.phone_number
WHERE (cp.country_code IS NULL OR cp.country_code = '');

-- Step 3: bound_phone 兜底（老数据可能只填了 bound_phone 没钱包绑定）
UPDATE `cloud_phone_pool`
SET `phone_number` = `bound_phone`
WHERE (`phone_number` IS NULL OR `phone_number` = '')
  AND `bound_phone` IS NOT NULL AND `bound_phone` != '';

-- Step 4: 兜底默认值
UPDATE `cloud_phone_pool` SET `country_code` = '62' WHERE `country_code` IS NULL OR `country_code` = '';
UPDATE `cloud_phone_pool` SET `phone_number` = ''   WHERE `phone_number` IS NULL;

-- Step 5: 改为 NOT NULL DEFAULT
ALTER TABLE `cloud_phone_pool`
  MODIFY COLUMN `country_code` VARCHAR(8)  NOT NULL DEFAULT '62' COMMENT '国家代码不带 +（默认 62 印尼）',
  MODIFY COLUMN `phone_number` VARCHAR(32) NOT NULL DEFAULT ''   COMMENT '手机号 E.164 不带 + 不带国家码（GoPay/WhatsApp 一号一机）';

-- Step 6: 删 bound_phone 列 + 索引
ALTER TABLE `cloud_phone_pool` DROP INDEX `idx_bound_phone`;
ALTER TABLE `cloud_phone_pool` DROP COLUMN `bound_phone`;
ALTER TABLE `cloud_phone_pool` ADD INDEX `idx_phone` (`country_code`, `phone_number`);

-- Step 7: gopay_wallet_pool 删唯一索引 + 两列
ALTER TABLE `gopay_wallet_pool` DROP INDEX `uk_phone`;
ALTER TABLE `gopay_wallet_pool`
  DROP COLUMN `country_code`,
  DROP COLUMN `phone_number`;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- 注意：down 只是把列加回去，无法还原原值（数据已搬到 cloud_phone）。
-- 如果真的要回滚，需要先用 SELECT cp.country_code, cp.phone_number FROM cloud_phone_pool cp
-- INNER JOIN gopay_wallet_pool w ON w.cloud_phone_id = cp.id WHERE w.deleted_at IS NULL;
-- 再手工写回 wallet。

ALTER TABLE `gopay_wallet_pool`
  ADD COLUMN `country_code` VARCHAR(8)  NOT NULL DEFAULT '62' AFTER `id`,
  ADD COLUMN `phone_number` VARCHAR(32) NOT NULL DEFAULT ''   AFTER `country_code`;
ALTER TABLE `gopay_wallet_pool` ADD UNIQUE KEY `uk_phone` (`country_code`, `phone_number`);

ALTER TABLE `cloud_phone_pool` DROP INDEX `idx_phone`;
ALTER TABLE `cloud_phone_pool`
  ADD COLUMN `bound_phone` VARCHAR(32) DEFAULT NULL COMMENT '反查：这台机里 WhatsApp 绑定的手机号' AFTER `prefer_api`;
ALTER TABLE `cloud_phone_pool` ADD INDEX `idx_bound_phone` (`bound_phone`);
ALTER TABLE `cloud_phone_pool`
  DROP COLUMN `country_code`,
  DROP COLUMN `phone_number`;

-- +goose StatementEnd
