-- +goose Up
-- +goose StatementBegin
--
-- 2026-05-13 模型分档计费：
--   - 图片：gpt-image-2 / nano-banana / nano-banana-v2 / nano-banana-pro
--     给每个 row 加 image_pricing = {1k, 2k, 4k → points*100}
--   - 视频：grok-imagine-video / vid-i2v 给 video_pricing = {6, 10, 20, 30 → points*100}
--     并把 video_pricing_mode 改为 "variant"（优先 map，map miss 再退到 scaled）
--
-- 思路：和 image_video_cleanup.sql 同样的 JSON_SEARCH 找路径 → JSON_SET 改字段。
-- 已经存档的行才打补丁；缺行的部分由 generation_handler.defaultPublicModels() 兜底返回。
-- 任何字段已经存在不为 NULL 的行也会被覆盖，确保 admin 后台之前手改过的非分档价
-- 重置回新阶梯（admin 后台后续仍可改）。
-- +goose StatementEnd

-- +goose StatementBegin
-- 1) gpt-image-2 → image_pricing
UPDATE `system_config`
SET `value` = JSON_SET(
  CAST(`value` AS JSON),
  REPLACE(JSON_UNQUOTE(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'gpt-image-2', NULL, '$[*].model_code')), '.model_code', '.image_pricing'),
  JSON_OBJECT('1k', 400, '2k', 1500, '4k', 3000)
)
WHERE `key` = 'billing.model_prices' AND JSON_VALID(`value`)
  AND JSON_SEARCH(CAST(`value` AS JSON), 'one', 'gpt-image-2', NULL, '$[*].model_code') IS NOT NULL;
-- +goose StatementEnd

-- +goose StatementBegin
-- 2) nano-banana → image_pricing
UPDATE `system_config`
SET `value` = JSON_SET(
  CAST(`value` AS JSON),
  REPLACE(JSON_UNQUOTE(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'nano-banana', NULL, '$[*].model_code')), '.model_code', '.image_pricing'),
  JSON_OBJECT('1k', 800, '2k', 1500, '4k', 3000)
)
WHERE `key` = 'billing.model_prices' AND JSON_VALID(`value`)
  AND JSON_SEARCH(CAST(`value` AS JSON), 'one', 'nano-banana', NULL, '$[*].model_code') IS NOT NULL;
-- +goose StatementEnd

-- +goose StatementBegin
-- 3) nano-banana-v2 → image_pricing
UPDATE `system_config`
SET `value` = JSON_SET(
  CAST(`value` AS JSON),
  REPLACE(JSON_UNQUOTE(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'nano-banana-v2', NULL, '$[*].model_code')), '.model_code', '.image_pricing'),
  JSON_OBJECT('1k', 800, '2k', 1500, '4k', 3000)
)
WHERE `key` = 'billing.model_prices' AND JSON_VALID(`value`)
  AND JSON_SEARCH(CAST(`value` AS JSON), 'one', 'nano-banana-v2', NULL, '$[*].model_code') IS NOT NULL;
-- +goose StatementEnd

-- +goose StatementBegin
-- 4) nano-banana-pro → image_pricing
UPDATE `system_config`
SET `value` = JSON_SET(
  CAST(`value` AS JSON),
  REPLACE(JSON_UNQUOTE(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'nano-banana-pro', NULL, '$[*].model_code')), '.model_code', '.image_pricing'),
  JSON_OBJECT('1k', 1500, '2k', 3000, '4k', 6000)
)
WHERE `key` = 'billing.model_prices' AND JSON_VALID(`value`)
  AND JSON_SEARCH(CAST(`value` AS JSON), 'one', 'nano-banana-pro', NULL, '$[*].model_code') IS NOT NULL;
-- +goose StatementEnd

-- +goose StatementBegin
-- 5) grok-imagine-video → video_pricing + video_pricing_mode=variant
UPDATE `system_config`
SET `value` = JSON_SET(
  CAST(`value` AS JSON),
  REPLACE(JSON_UNQUOTE(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'grok-imagine-video', NULL, '$[*].model_code')), '.model_code', '.video_pricing'),
  JSON_OBJECT('6', 1500, '10', 2500, '20', 5000, '30', 7500),
  REPLACE(JSON_UNQUOTE(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'grok-imagine-video', NULL, '$[*].model_code')), '.model_code', '.video_pricing_mode'),
  'variant'
)
WHERE `key` = 'billing.model_prices' AND JSON_VALID(`value`)
  AND JSON_SEARCH(CAST(`value` AS JSON), 'one', 'grok-imagine-video', NULL, '$[*].model_code') IS NOT NULL;
-- +goose StatementEnd

-- +goose StatementBegin
-- 6) vid-i2v → video_pricing + video_pricing_mode=variant
UPDATE `system_config`
SET `value` = JSON_SET(
  CAST(`value` AS JSON),
  REPLACE(JSON_UNQUOTE(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'vid-i2v', NULL, '$[*].model_code')), '.model_code', '.video_pricing'),
  JSON_OBJECT('6', 2000, '10', 3000, '20', 6000, '30', 9000),
  REPLACE(JSON_UNQUOTE(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'vid-i2v', NULL, '$[*].model_code')), '.model_code', '.video_pricing_mode'),
  'variant'
)
WHERE `key` = 'billing.model_prices' AND JSON_VALID(`value`)
  AND JSON_SEARCH(CAST(`value` AS JSON), 'one', 'vid-i2v', NULL, '$[*].model_code') IS NOT NULL;
-- +goose StatementEnd

-- +goose StatementBegin
-- 7) vid-v1 → video_pricing + video_pricing_mode=variant（如果还在的话）
UPDATE `system_config`
SET `value` = JSON_SET(
  CAST(`value` AS JSON),
  REPLACE(JSON_UNQUOTE(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'vid-v1', NULL, '$[*].model_code')), '.model_code', '.video_pricing'),
  JSON_OBJECT('6', 1500, '10', 2500, '20', 5000, '30', 7500),
  REPLACE(JSON_UNQUOTE(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'vid-v1', NULL, '$[*].model_code')), '.model_code', '.video_pricing_mode'),
  'variant'
)
WHERE `key` = 'billing.model_prices' AND JSON_VALID(`value`)
  AND JSON_SEARCH(CAST(`value` AS JSON), 'one', 'vid-v1', NULL, '$[*].model_code') IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- 不回滚：image_pricing / video_pricing 字段是新增，旧代码会被 json.Unmarshal 忽略，
-- 留在 JSON 里也不会产生副作用。video_pricing_mode 退回 scaled 也不影响 base 价。
-- 如需手动清理，运行：
--   UPDATE system_config SET value = JSON_REMOVE(value, '$[N].image_pricing') WHERE key='billing.model_prices';
-- +goose StatementEnd
