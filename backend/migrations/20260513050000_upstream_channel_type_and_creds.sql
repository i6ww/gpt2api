-- +goose Up
-- +goose StatementBegin
-- 上游通道二元类型化：
--   channel_type = 'local_pool'   仅 1 行，对应"本地号池"。
--                                 系统根据请求 model→provider 自动选号池
--                                 （pool_gpt / pool_grok / pool_adobe）。
--                                 不需要 api_key / base_url。
--   channel_type = 'external_api' N 行，每行对应一个第三方付费 API。
--                                 自带 api_key + base_url + 支持的 model 列表。
--                                 runtime 用 OpenAI-compat 协议转发请求。
--
-- api_key_enc 走 AES-GCM 加密（同 account.credential_enc 用同一把主密钥）。
-- supported_models 是一个 JSON 数组：["gpt-image-2","gpt-4o","grok-4-fast"]。
--   local_pool 行留空（数组），含义是"系统识别全部已知 model_code"。
ALTER TABLE `upstream_channel`
  ADD COLUMN `channel_type` VARCHAR(16) NOT NULL DEFAULT 'external_api' AFTER `route`,
  ADD COLUMN `api_key_enc` BLOB NULL AFTER `unit_price`,
  ADD COLUMN `supported_models` JSON NULL AFTER `capabilities`,
  ADD INDEX `idx_channel_type` (`channel_type`);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE `upstream_channel`
  DROP INDEX `idx_channel_type`,
  DROP COLUMN `supported_models`,
  DROP COLUMN `api_key_enc`,
  DROP COLUMN `channel_type`;
-- +goose StatementEnd
