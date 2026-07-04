-- +goose Up
-- +goose StatementBegin
--
-- FF-iOS 受信任设备 token。okad 注册/激活账号后导出 device_token + device_id，
-- 本平台用 ims/token/v4 grant_type=device 免验证码刷新 24h access_token，减少
-- Firefly 伪 408 "system under load"。
--

ALTER TABLE `pool_adobe`
  ADD COLUMN `device_token_enc` BLOB NULL COMMENT '加密后的 FF-iOS device_token' AFTER `cookie_enc`,
  ADD COLUMN `device_id` VARCHAR(64) DEFAULT NULL COMMENT 'FF-iOS 原始 device_id(UUID)' AFTER `device_token_enc`;

-- +goose StatementEnd

-- +goose Down
ALTER TABLE `pool_adobe`
  DROP COLUMN `device_id`,
  DROP COLUMN `device_token_enc`;
