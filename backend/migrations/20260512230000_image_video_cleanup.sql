-- +goose Up
-- +goose StatementBegin
--
-- 2026-05-12 模型清单收拢：
--   - 图像：GPT-image-2（web 1K 路径）+ Adobe Nano Banana 三件套
--   - 视频：仅 GROK（grok-imagine-video + vid-v1 + vid-i2v）
--   - 文字：仅 GROK（gpt-4o-mini 不接入，免账号需 billing credit）
--
-- 处理目标：
--   1. billing.model_prices 里把以下旧/暂不可用条目置 enabled=false：
--        img-3d / img-anime / img-real / img-v3（被 gpt-image-2 取代）
--        gpt-4o-mini（账号无 billing credit 跑不通）
--        sora2 / sora2-pro / veo3.1 / veo3.1-fast / veo3.1-ref（视频统一 GROK）
--   2. billing.model_prices 缺失则补 gpt-image-2 / nano-banana-* 行（已有跳过）。
--   3. model 表里这些旧条目 status=0（保留行便于回滚），补 gpt-image-2。
--
-- 不删除任何记录，仅 disable 状态，让管理后台仍可看到 + 一键开回。
-- +goose StatementEnd

-- +goose StatementBegin
-- 1) billing.model_prices: 旧条目 enabled=false
--    思路：JSON_SEARCH 找到 model_code 所在 $[i].model_code 路径，
--    REPLACE 把 .model_code 换成 .enabled，JSON_SET 把那一格置 false。
UPDATE `system_config`
SET `value` = JSON_SET(
  CAST(`value` AS JSON),
  REPLACE(JSON_UNQUOTE(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'img-3d', NULL, '$[*].model_code')), '.model_code', '.enabled'),
  CAST('false' AS JSON)
)
WHERE `key` = 'billing.model_prices' AND JSON_VALID(`value`)
  AND JSON_SEARCH(CAST(`value` AS JSON), 'one', 'img-3d', NULL, '$[*].model_code') IS NOT NULL;

UPDATE `system_config`
SET `value` = JSON_SET(
  CAST(`value` AS JSON),
  REPLACE(JSON_UNQUOTE(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'img-anime', NULL, '$[*].model_code')), '.model_code', '.enabled'),
  CAST('false' AS JSON)
)
WHERE `key` = 'billing.model_prices' AND JSON_VALID(`value`)
  AND JSON_SEARCH(CAST(`value` AS JSON), 'one', 'img-anime', NULL, '$[*].model_code') IS NOT NULL;

UPDATE `system_config`
SET `value` = JSON_SET(
  CAST(`value` AS JSON),
  REPLACE(JSON_UNQUOTE(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'img-real', NULL, '$[*].model_code')), '.model_code', '.enabled'),
  CAST('false' AS JSON)
)
WHERE `key` = 'billing.model_prices' AND JSON_VALID(`value`)
  AND JSON_SEARCH(CAST(`value` AS JSON), 'one', 'img-real', NULL, '$[*].model_code') IS NOT NULL;

UPDATE `system_config`
SET `value` = JSON_SET(
  CAST(`value` AS JSON),
  REPLACE(JSON_UNQUOTE(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'img-v3', NULL, '$[*].model_code')), '.model_code', '.enabled'),
  CAST('false' AS JSON)
)
WHERE `key` = 'billing.model_prices' AND JSON_VALID(`value`)
  AND JSON_SEARCH(CAST(`value` AS JSON), 'one', 'img-v3', NULL, '$[*].model_code') IS NOT NULL;

UPDATE `system_config`
SET `value` = JSON_SET(
  CAST(`value` AS JSON),
  REPLACE(JSON_UNQUOTE(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'gpt-4o-mini', NULL, '$[*].model_code')), '.model_code', '.enabled'),
  CAST('false' AS JSON)
)
WHERE `key` = 'billing.model_prices' AND JSON_VALID(`value`)
  AND JSON_SEARCH(CAST(`value` AS JSON), 'one', 'gpt-4o-mini', NULL, '$[*].model_code') IS NOT NULL;

UPDATE `system_config`
SET `value` = JSON_SET(
  CAST(`value` AS JSON),
  REPLACE(JSON_UNQUOTE(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'sora2', NULL, '$[*].model_code')), '.model_code', '.enabled'),
  CAST('false' AS JSON)
)
WHERE `key` = 'billing.model_prices' AND JSON_VALID(`value`)
  AND JSON_SEARCH(CAST(`value` AS JSON), 'one', 'sora2', NULL, '$[*].model_code') IS NOT NULL;

UPDATE `system_config`
SET `value` = JSON_SET(
  CAST(`value` AS JSON),
  REPLACE(JSON_UNQUOTE(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'sora2-pro', NULL, '$[*].model_code')), '.model_code', '.enabled'),
  CAST('false' AS JSON)
)
WHERE `key` = 'billing.model_prices' AND JSON_VALID(`value`)
  AND JSON_SEARCH(CAST(`value` AS JSON), 'one', 'sora2-pro', NULL, '$[*].model_code') IS NOT NULL;

UPDATE `system_config`
SET `value` = JSON_SET(
  CAST(`value` AS JSON),
  REPLACE(JSON_UNQUOTE(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'veo3.1', NULL, '$[*].model_code')), '.model_code', '.enabled'),
  CAST('false' AS JSON)
)
WHERE `key` = 'billing.model_prices' AND JSON_VALID(`value`)
  AND JSON_SEARCH(CAST(`value` AS JSON), 'one', 'veo3.1', NULL, '$[*].model_code') IS NOT NULL;

UPDATE `system_config`
SET `value` = JSON_SET(
  CAST(`value` AS JSON),
  REPLACE(JSON_UNQUOTE(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'veo3.1-ref', NULL, '$[*].model_code')), '.model_code', '.enabled'),
  CAST('false' AS JSON)
)
WHERE `key` = 'billing.model_prices' AND JSON_VALID(`value`)
  AND JSON_SEARCH(CAST(`value` AS JSON), 'one', 'veo3.1-ref', NULL, '$[*].model_code') IS NOT NULL;

UPDATE `system_config`
SET `value` = JSON_SET(
  CAST(`value` AS JSON),
  REPLACE(JSON_UNQUOTE(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'veo3.1-fast', NULL, '$[*].model_code')), '.model_code', '.enabled'),
  CAST('false' AS JSON)
)
WHERE `key` = 'billing.model_prices' AND JSON_VALID(`value`)
  AND JSON_SEARCH(CAST(`value` AS JSON), 'one', 'veo3.1-fast', NULL, '$[*].model_code') IS NOT NULL;
-- +goose StatementEnd

-- +goose StatementBegin
-- 2) billing.model_prices: 缺 gpt-image-2 行就追加（unit_points=0 表示按笔计费，
--    实际计费由生成服务读取 model 表的 point_per_unit + 数量）。
UPDATE `system_config`
SET `value` = JSON_MERGE_PRESERVE(
  CAST(`value` AS JSON),
  JSON_ARRAY(JSON_OBJECT(
    'model_code','gpt-image-2',
    'name','GPT Image 2',
    'kind','image',
    'provider','gpt',
    'upstream_model','gpt-image-2',
    'unit_points',0,
    'enabled',TRUE
  ))
)
WHERE `key` = 'billing.model_prices'
  AND JSON_VALID(`value`)
  AND ISNULL(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'gpt-image-2', NULL, '$[*].model_code'));
-- +goose StatementEnd

-- +goose StatementBegin
-- 3) model 表：旧/暂不可用条目 status=0（保留便于回滚）。
UPDATE `model` SET `status` = 0
WHERE `code` IN (
  'img-3d','img-anime','img-real','img-v3',
  'gpt-4o-mini',
  'sora2','sora2-pro','veo3.1','veo3.1-ref','veo3.1-fast'
) AND `deleted_at` IS NULL;

-- 4) model 表补 gpt-image-2 行（默认 1 张图 = 4 点，按 newbanana image_gpt 1K $0.04 估算）。
INSERT INTO `account_group` (`provider`, `code`, `name`, `strategy`)
VALUES ('gpt', 'gpt-image-default', 'GPT 图像通用', 'round_robin')
ON DUPLICATE KEY UPDATE `name`=VALUES(`name`);

INSERT INTO `model`
  (`code`, `name`, `kind`, `provider`, `version`, `tags`, `point_per_unit`, `unit`, `group_code`, `min_plan`, `is_hot`, `sort`)
VALUES
  ('gpt-image-2','GPT Image 2','image','gpt','web-1k','gpt,image,web,1k',4,'image','gpt-image-default','free',1,10)
ON DUPLICATE KEY UPDATE
  `name`=VALUES(`name`),
  `kind`=VALUES(`kind`),
  `provider`=VALUES(`provider`),
  `version`=VALUES(`version`),
  `tags`=VALUES(`tags`),
  `point_per_unit`=VALUES(`point_per_unit`),
  `unit`=VALUES(`unit`),
  `group_code`=VALUES(`group_code`),
  `status`=1;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- 还原仅做最小工作：把 model 表里被关掉的几个条目重新 status=1，
-- billing.model_prices 不回滚（不影响 fallback 路径）。
UPDATE `model` SET `status` = 1
WHERE `code` IN (
  'img-3d','img-anime','img-real','img-v3',
  'gpt-4o-mini',
  'sora2','sora2-pro','veo3.1','veo3.1-ref','veo3.1-fast'
);
-- 不删 gpt-image-2 / gpt-image-default —— 这些 forward 才有的，回滚保留更安全。
-- +goose StatementEnd
