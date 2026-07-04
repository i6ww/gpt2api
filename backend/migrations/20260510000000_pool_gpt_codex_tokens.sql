-- +goose Up
-- +goose StatementBegin

ALTER TABLE `pool_gpt`
  ADD COLUMN `id_token_enc` BLOB DEFAULT NULL COMMENT 'AES-256-GCM 加密 id_token (Codex CLI / OAuth)' AFTER `refresh_token_enc`,
  ADD COLUMN `api_key_enc`  BLOB DEFAULT NULL COMMENT 'AES-256-GCM 加密 OpenAI API Key (Codex CLI token-exchange 产物)' AFTER `id_token_enc`;

-- 回填默认 issuer / client_id（保持向后兼容）
UPDATE `pool_gpt`
   SET `oauth_issuer` = 'https://auth.openai.com'
 WHERE `oauth_issuer` IS NULL OR `oauth_issuer` = '';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE `pool_gpt`
  DROP COLUMN `id_token_enc`,
  DROP COLUMN `api_key_enc`;

-- +goose StatementEnd
