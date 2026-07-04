-- +goose Up
-- +goose StatementBegin
--
-- 2026-05-13 修复：billing.model_prices 里 image 模型的 upstream_model 之前被写成
-- 固定的 catalog SKU（"firefly-nano-banana-2k-16x9" 等），导致 firefly.ResolvePublicAlias
-- 第一行 `if _, ok := Catalog[modelID]; ok { return modelID }` 直接命中、跳过
-- 「公开 alias → ratio + resolution → SKU」的真正解析，所有 1K/4K/比例 都被钉死成 2K。
--
-- 改法：把 upstream_model 清空（NULL/""）。GenerationService.upstreamModel 在
-- upstream_model 空时返回原始 modelCode，公开 alias（"nano-banana" / "nano-banana-v2"
-- / "nano-banana-pro" / "gpt-image-2"）原样进 Adobe Provider → ResolvePublicAlias 走
-- publicModelAliases 分支 → 按 size + quality 拼出正确 SKU。
--
-- 单条 UPDATE：先用 JSON_REPLACE + JSON_SEARCH 定位每个 model_code 行的
-- $.upstream_model 字段，把它替换成 ""。无副作用，幂等。
UPDATE `system_config`
SET `value` = JSON_REPLACE(
  CAST(`value` AS JSON),
  REPLACE(JSON_UNQUOTE(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'nano-banana-pro', NULL, '$[*].model_code')), '.model_code', '.upstream_model'),
  ''
)
WHERE `key` = 'billing.model_prices' AND JSON_VALID(`value`)
  AND JSON_SEARCH(CAST(`value` AS JSON), 'one', 'nano-banana-pro', NULL, '$[*].model_code') IS NOT NULL;

UPDATE `system_config`
SET `value` = JSON_REPLACE(
  CAST(`value` AS JSON),
  REPLACE(JSON_UNQUOTE(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'nano-banana-v2', NULL, '$[*].model_code')), '.model_code', '.upstream_model'),
  ''
)
WHERE `key` = 'billing.model_prices' AND JSON_VALID(`value`)
  AND JSON_SEARCH(CAST(`value` AS JSON), 'one', 'nano-banana-v2', NULL, '$[*].model_code') IS NOT NULL;

UPDATE `system_config`
SET `value` = JSON_REPLACE(
  CAST(`value` AS JSON),
  REPLACE(JSON_UNQUOTE(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'nano-banana', NULL, '$[*].model_code')), '.model_code', '.upstream_model'),
  ''
)
WHERE `key` = 'billing.model_prices' AND JSON_VALID(`value`)
  AND JSON_SEARCH(CAST(`value` AS JSON), 'one', 'nano-banana', NULL, '$[*].model_code') IS NOT NULL;

-- gpt-image-2 之前被写成 "gpt-image"，导致 GPT provider 的 isGPTImage2() 不命中、
-- 没法走 web 路径（generateImage2Web），同时 Adobe 走过来时也找不到 alias 因为
-- publicModelAliases 里只有 "gpt-image-2" 没有 "gpt-image"。统一改回 ""。
UPDATE `system_config`
SET `value` = JSON_REPLACE(
  CAST(`value` AS JSON),
  REPLACE(JSON_UNQUOTE(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'gpt-image-2', NULL, '$[*].model_code')), '.model_code', '.upstream_model'),
  ''
)
WHERE `key` = 'billing.model_prices' AND JSON_VALID(`value`)
  AND JSON_SEARCH(CAST(`value` AS JSON), 'one', 'gpt-image-2', NULL, '$[*].model_code') IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- 还原成历史 catalog SKU（实际上业务侧不会回滚，仅供 goose down 一致性）。
UPDATE `system_config`
SET `value` = JSON_REPLACE(
  CAST(`value` AS JSON),
  REPLACE(JSON_UNQUOTE(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'nano-banana-pro', NULL, '$[*].model_code')), '.model_code', '.upstream_model'),
  'firefly-nano-banana-pro-2k-16x9'
)
WHERE `key` = 'billing.model_prices' AND JSON_VALID(`value`)
  AND JSON_SEARCH(CAST(`value` AS JSON), 'one', 'nano-banana-pro', NULL, '$[*].model_code') IS NOT NULL;
-- +goose StatementEnd
