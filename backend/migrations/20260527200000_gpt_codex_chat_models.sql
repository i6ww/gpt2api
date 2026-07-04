-- +goose Up
-- +goose StatementBegin
INSERT INTO `account_group` (`provider`, `code`, `name`, `strategy`)
VALUES ('gpt', 'gpt-chat-codex-default', 'GPT Codex 文字', 'round_robin')
ON DUPLICATE KEY UPDATE `name`=VALUES(`name`);

INSERT INTO `model` (`code`, `name`, `kind`, `provider`, `version`, `tags`, `point_per_unit`, `unit`, `group_code`, `min_plan`, `is_hot`, `sort`)
VALUES
('gpt-5.4', 'GPT 5.4', 'text', 'gpt', 'chat', '文字,对话,GPT,Codex', 200, '1k_token', 'gpt-chat-codex-default', 'free', 1, 5),
('gpt-5.4-mini', 'GPT 5.4 Mini', 'text', 'gpt', 'chat', '文字,对话,GPT,Codex', 100, '1k_token', 'gpt-chat-codex-default', 'free', 1, 6),
('gpt-5.3-codex', 'GPT 5.3 Codex', 'text', 'gpt', 'chat', '文字,对话,GPT,Codex', 150, '1k_token', 'gpt-chat-codex-default', 'free', 0, 7)
ON DUPLICATE KEY UPDATE
`name`=VALUES(`name`), `kind`=VALUES(`kind`), `provider`=VALUES(`provider`), `version`=VALUES(`version`),
`tags`=VALUES(`tags`), `point_per_unit`=VALUES(`point_per_unit`), `unit`=VALUES(`unit`), `group_code`=VALUES(`group_code`), `status`=1;

UPDATE `system_config`
SET `value` = JSON_ARRAY_APPEND(CAST(`value` AS JSON), '$', CAST('{"model_code":"gpt-5.4","name":"GPT 5.4","kind":"text","provider":"gpt","upstream_model":"gpt-5.4","unit_points":0,"input_unit_points":200,"output_unit_points":600,"enabled":true}' AS JSON))
WHERE `key`='billing.model_prices' AND JSON_VALID(`value`)
  AND JSON_SEARCH(CAST(`value` AS JSON), 'one', 'gpt-5.4', NULL, '$[*].model_code') IS NULL;

UPDATE `system_config`
SET `value` = JSON_ARRAY_APPEND(CAST(`value` AS JSON), '$', CAST('{"model_code":"gpt-5.4-mini","name":"GPT 5.4 Mini","kind":"text","provider":"gpt","upstream_model":"gpt-5.4-mini","unit_points":0,"input_unit_points":100,"output_unit_points":300,"enabled":true}' AS JSON))
WHERE `key`='billing.model_prices' AND JSON_VALID(`value`)
  AND JSON_SEARCH(CAST(`value` AS JSON), 'one', 'gpt-5.4-mini', NULL, '$[*].model_code') IS NULL;

UPDATE `system_config`
SET `value` = JSON_ARRAY_APPEND(CAST(`value` AS JSON), '$', CAST('{"model_code":"gpt-5.3-codex","name":"GPT 5.3 Codex","kind":"text","provider":"gpt","upstream_model":"gpt-5.3-codex","unit_points":0,"input_unit_points":150,"output_unit_points":450,"enabled":true}' AS JSON))
WHERE `key`='billing.model_prices' AND JSON_VALID(`value`)
  AND JSON_SEARCH(CAST(`value` AS JSON), 'one', 'gpt-5.3-codex', NULL, '$[*].model_code') IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
UPDATE `model` SET `status` = 0
WHERE `code` IN ('gpt-5.4', 'gpt-5.4-mini', 'gpt-5.3-codex');
-- billing.model_prices 不回滚，避免影响已保存的运营配置。
-- +goose StatementEnd
