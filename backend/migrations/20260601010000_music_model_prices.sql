-- +goose Up
-- +goose StatementBegin
--
-- 把 FlowMusic 音乐模型 (lyria / lyria-pro) 注册到 billing.model_prices system_config，
-- 这样 /api/v1/models 能返、/api/v1/gen/music 能路由到 provider="flowmusic"，
-- GenerationService.MusicProviderForModel 也能命中。
--
-- 价格单位 unit_points = cent-points（1 unit_point = 1/100 点）：
--   lyria      = 20 点 → 2000
--   lyria-pro  = 30 点 → 3000
-- 一首歌一次任务（count=1），admin 后台可改。
--
-- 不存在 billing.model_prices 行：不在这里整表插入（adobe 迁移已建表）。
-- 已存在：按 model_code 检测，缺失则用 JSON_MERGE_PRESERVE 追加。
UPDATE `system_config`
SET `value` = JSON_MERGE_PRESERVE(
  CAST(`value` AS JSON),
  JSON_ARRAY(JSON_OBJECT('model_code','lyria','name','Lyria','kind','music','provider','flowmusic','upstream_model','producer:standard','unit_points',2000,'enabled',TRUE))
)
WHERE `key` = 'billing.model_prices'
  AND JSON_VALID(`value`)
  AND ISNULL(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'lyria', NULL, '$[*].model_code'));

UPDATE `system_config`
SET `value` = JSON_MERGE_PRESERVE(
  CAST(`value` AS JSON),
  JSON_ARRAY(JSON_OBJECT('model_code','lyria-pro','name','Lyria Pro','kind','music','provider','flowmusic','upstream_model','producer:standard','unit_points',3000,'enabled',TRUE))
)
WHERE `key` = 'billing.model_prices'
  AND JSON_VALID(`value`)
  AND ISNULL(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'lyria-pro', NULL, '$[*].model_code'));
-- +goose StatementEnd

-- +goose StatementBegin
--
-- 给 model 表（admin UI 列表）补音乐行，方便后台查看 / 编辑价格。
-- point_per_unit 单位是「点」，按 unit_points / 100 取整。
INSERT INTO `account_group` (`provider`, `code`, `name`, `strategy`)
VALUES ('flowmusic', 'flowmusic-default', 'FlowMusic 通用', 'round_robin')
ON DUPLICATE KEY UPDATE `name`=VALUES(`name`);

INSERT INTO `model` (`code`, `name`, `kind`, `provider`, `version`, `tags`, `point_per_unit`, `unit`, `group_code`, `min_plan`, `is_hot`, `sort`)
VALUES
  ('lyria','Lyria','music','flowmusic','standard','flowmusic,music,song',20,'music','flowmusic-default','free',1,40),
  ('lyria-pro','Lyria Pro','music','flowmusic','pro','flowmusic,music,song,pro',30,'music','flowmusic-default','plus',0,41)
ON DUPLICATE KEY UPDATE
  `name`=VALUES(`name`),
  `kind`=VALUES(`kind`),
  `provider`=VALUES(`provider`),
  `version`=VALUES(`version`),
  `tags`=VALUES(`tags`),
  `point_per_unit`=VALUES(`point_per_unit`),
  `unit`=VALUES(`unit`),
  `group_code`=VALUES(`group_code`);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM `model` WHERE `code` IN ('lyria','lyria-pro');
DELETE FROM `account_group` WHERE `code` = 'flowmusic-default';
-- +goose StatementEnd
