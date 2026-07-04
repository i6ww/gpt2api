-- +goose Up
-- +goose StatementBegin
--
-- 把 Adobe Firefly 公开 alias 注册到 billing.model_prices system_config 里，
-- 这样 /v1/models 能返、/v1/images/generations + /v1/video/generations 能路由
-- 到 provider="adobe"，GenerationService.ImageProviderForModel /
-- VideoProviderForModel 也能命中。
--
-- 价格按 newbanana 现成 schema（pricing_config.image_pro / image_std / image_gpt
-- / video）的 2K 档为基准换算成 newgpt2api 的 unit_points（cent-points，
-- 1 unit_point = $0.01 / 100 = 1/100 point）：
--   image_std  2K = $0.15  → 1500
--   image_pro  2K = $0.30  → 3000
--   image_gpt  2K = $0.20  → 2000
--   sora2       = $1.00  → 10000
--   sora2-pro   = $2.00  → 20000
--   veo3.1      = $1.50  → 15000
--   veo3.1-ref  = $2.00  → 20000
--   veo3.1-fast = $1.00  → 10000
--
-- 不存在 billing.model_prices 行：直接插入完整 JSON 数组（包含已有 gpt/grok
-- + 新增 adobe）。已存在：用 JSON_MERGE_PRESERVE 把 Adobe 9 条追加进去。
INSERT INTO `system_config` (`key`, `value`, `remark`)
SELECT 'billing.model_prices',
       JSON_ARRAY(
         JSON_OBJECT('model_code','gpt-4o-mini','name','文字对话','kind','text','provider','gpt','upstream_model','gpt-4o-mini','unit_points',0,'input_unit_points',100,'output_unit_points',300,'enabled',TRUE),
         JSON_OBJECT('model_code','gpt-image-2','name','GPT IMAGE 2','kind','image','provider','gpt','upstream_model','gpt-image','unit_points',400,'enabled',TRUE),
         JSON_OBJECT('model_code','nano-banana-pro','name','Nano Banana Pro','kind','image','provider','adobe','upstream_model','firefly-nano-banana-pro-2k-16x9','unit_points',3000,'enabled',TRUE),
         JSON_OBJECT('model_code','nano-banana-v2','name','Nano Banana V2','kind','image','provider','adobe','upstream_model','firefly-nano-banana2-2k-16x9','unit_points',1500,'enabled',TRUE),
         JSON_OBJECT('model_code','nano-banana','name','Nano Banana','kind','image','provider','adobe','upstream_model','firefly-nano-banana-2k-16x9','unit_points',1500,'enabled',TRUE),
         JSON_OBJECT('model_code','sora2','name','Sora2','kind','video','provider','adobe','upstream_model','firefly-sora2-8s-16x9','unit_points',10000,'video_pricing_mode','flat','enabled',TRUE),
         JSON_OBJECT('model_code','sora2-pro','name','Sora2 Pro','kind','video','provider','adobe','upstream_model','firefly-sora2-pro-8s-16x9','unit_points',20000,'video_pricing_mode','flat','enabled',TRUE),
         JSON_OBJECT('model_code','veo3.1','name','Veo 3.1','kind','video','provider','adobe','upstream_model','firefly-veo31-6s-16x9-1080p','unit_points',15000,'video_pricing_mode','flat','enabled',TRUE),
         JSON_OBJECT('model_code','veo3.1-ref','name','Veo 3.1 Ref','kind','video','provider','adobe','upstream_model','firefly-veo31-ref-8s-16x9-1080p','unit_points',20000,'video_pricing_mode','flat','enabled',TRUE),
         JSON_OBJECT('model_code','veo3.1-fast','name','Veo 3.1 Fast','kind','video','provider','adobe','upstream_model','firefly-veo31-fast-6s-16x9-1080p','unit_points',10000,'video_pricing_mode','flat','enabled',TRUE)
       ),
       '模型价格、上游映射和文字 token 计费（含 Adobe Firefly 公开 alias）'
WHERE NOT EXISTS (SELECT 1 FROM `system_config` WHERE `key`='billing.model_prices');
-- +goose StatementEnd

-- +goose StatementBegin
--
-- 行已存在的情况：按 model_code 检测，缺失则追加。
-- JSON_SEARCH 在数组里找不到时返回 NULL，我们用 ISNULL() 判定是否需要追加。
UPDATE `system_config`
SET `value` = JSON_MERGE_PRESERVE(
  CAST(`value` AS JSON),
  JSON_ARRAY(JSON_OBJECT('model_code','nano-banana-pro','name','Nano Banana Pro','kind','image','provider','adobe','upstream_model','firefly-nano-banana-pro-2k-16x9','unit_points',3000,'enabled',TRUE))
)
WHERE `key` = 'billing.model_prices'
  AND JSON_VALID(`value`)
  AND ISNULL(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'nano-banana-pro', NULL, '$[*].model_code'));

UPDATE `system_config`
SET `value` = JSON_MERGE_PRESERVE(
  CAST(`value` AS JSON),
  JSON_ARRAY(JSON_OBJECT('model_code','nano-banana-v2','name','Nano Banana V2','kind','image','provider','adobe','upstream_model','firefly-nano-banana2-2k-16x9','unit_points',1500,'enabled',TRUE))
)
WHERE `key` = 'billing.model_prices'
  AND JSON_VALID(`value`)
  AND ISNULL(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'nano-banana-v2', NULL, '$[*].model_code'));

UPDATE `system_config`
SET `value` = JSON_MERGE_PRESERVE(
  CAST(`value` AS JSON),
  JSON_ARRAY(JSON_OBJECT('model_code','nano-banana','name','Nano Banana','kind','image','provider','adobe','upstream_model','firefly-nano-banana-2k-16x9','unit_points',1500,'enabled',TRUE))
)
WHERE `key` = 'billing.model_prices'
  AND JSON_VALID(`value`)
  AND ISNULL(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'nano-banana', NULL, '$[*].model_code'));

UPDATE `system_config`
SET `value` = JSON_MERGE_PRESERVE(
  CAST(`value` AS JSON),
  JSON_ARRAY(JSON_OBJECT('model_code','sora2','name','Sora2','kind','video','provider','adobe','upstream_model','firefly-sora2-8s-16x9','unit_points',10000,'video_pricing_mode','flat','enabled',TRUE))
)
WHERE `key` = 'billing.model_prices'
  AND JSON_VALID(`value`)
  AND ISNULL(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'sora2', NULL, '$[*].model_code'));

UPDATE `system_config`
SET `value` = JSON_MERGE_PRESERVE(
  CAST(`value` AS JSON),
  JSON_ARRAY(JSON_OBJECT('model_code','sora2-pro','name','Sora2 Pro','kind','video','provider','adobe','upstream_model','firefly-sora2-pro-8s-16x9','unit_points',20000,'video_pricing_mode','flat','enabled',TRUE))
)
WHERE `key` = 'billing.model_prices'
  AND JSON_VALID(`value`)
  AND ISNULL(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'sora2-pro', NULL, '$[*].model_code'));

UPDATE `system_config`
SET `value` = JSON_MERGE_PRESERVE(
  CAST(`value` AS JSON),
  JSON_ARRAY(JSON_OBJECT('model_code','veo3.1','name','Veo 3.1','kind','video','provider','adobe','upstream_model','firefly-veo31-6s-16x9-1080p','unit_points',15000,'video_pricing_mode','flat','enabled',TRUE))
)
WHERE `key` = 'billing.model_prices'
  AND JSON_VALID(`value`)
  AND ISNULL(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'veo3.1', NULL, '$[*].model_code'));

UPDATE `system_config`
SET `value` = JSON_MERGE_PRESERVE(
  CAST(`value` AS JSON),
  JSON_ARRAY(JSON_OBJECT('model_code','veo3.1-ref','name','Veo 3.1 Ref','kind','video','provider','adobe','upstream_model','firefly-veo31-ref-8s-16x9-1080p','unit_points',20000,'video_pricing_mode','flat','enabled',TRUE))
)
WHERE `key` = 'billing.model_prices'
  AND JSON_VALID(`value`)
  AND ISNULL(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'veo3.1-ref', NULL, '$[*].model_code'));

UPDATE `system_config`
SET `value` = JSON_MERGE_PRESERVE(
  CAST(`value` AS JSON),
  JSON_ARRAY(JSON_OBJECT('model_code','veo3.1-fast','name','Veo 3.1 Fast','kind','video','provider','adobe','upstream_model','firefly-veo31-fast-6s-16x9-1080p','unit_points',10000,'video_pricing_mode','flat','enabled',TRUE))
)
WHERE `key` = 'billing.model_prices'
  AND JSON_VALID(`value`)
  AND ISNULL(JSON_SEARCH(CAST(`value` AS JSON), 'one', 'veo3.1-fast', NULL, '$[*].model_code'));
-- +goose StatementEnd

-- +goose StatementBegin
--
-- 给 model 表（admin UI 列表）补 Adobe 行，方便后台查看 / 编辑价格。
-- 价格列 point_per_unit 单位是「点」（不是 cent-point），按 unit_points / 100 取整。
INSERT INTO `account_group` (`provider`, `code`, `name`, `strategy`)
VALUES ('adobe', 'adobe-firefly-default', 'Adobe Firefly 通用', 'round_robin')
ON DUPLICATE KEY UPDATE `name`=VALUES(`name`);

INSERT INTO `model` (`code`, `name`, `kind`, `provider`, `version`, `tags`, `point_per_unit`, `unit`, `group_code`, `min_plan`, `is_hot`, `sort`)
VALUES
  ('nano-banana-pro','Nano Banana Pro','image','adobe','firefly','adobe,nano-banana,image,2k',30,'image','adobe-firefly-default','plus',1,30),
  ('nano-banana-v2','Nano Banana V2','image','adobe','firefly','adobe,nano-banana,image,2k',15,'image','adobe-firefly-default','plus',0,31),
  ('nano-banana','Nano Banana','image','adobe','firefly','adobe,nano-banana,image,2k',15,'image','adobe-firefly-default','plus',0,32),
  ('sora2','Sora2','video','adobe','firefly','adobe,video,sora2',100,'video','adobe-firefly-default','plus',1,33),
  ('sora2-pro','Sora2 Pro','video','adobe','firefly','adobe,video,sora2,pro',200,'video','adobe-firefly-default','plus',0,34),
  ('veo3.1','Veo 3.1','video','adobe','firefly','adobe,video,veo3.1',150,'video','adobe-firefly-default','plus',0,35),
  ('veo3.1-ref','Veo 3.1 Ref','video','adobe','firefly','adobe,video,veo3.1,ref',200,'video','adobe-firefly-default','plus',0,36),
  ('veo3.1-fast','Veo 3.1 Fast','video','adobe','firefly','adobe,video,veo3.1,fast',100,'video','adobe-firefly-default','plus',0,37)
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
DELETE FROM `model` WHERE `code` IN (
  'nano-banana-pro','nano-banana-v2','nano-banana',
  'sora2','sora2-pro','veo3.1','veo3.1-ref','veo3.1-fast'
);
DELETE FROM `account_group` WHERE `code` = 'adobe-firefly-default';
-- +goose StatementEnd
