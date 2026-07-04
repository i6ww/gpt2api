-- +goose Up
-- +goose StatementBegin
-- 2026-05-14 多节点集群基础设施
--   1. cluster_node     节点注册 + HMAC 凭证 + 心跳元数据
--   2. download_locator 资源 (asset_key) 在哪些节点上有本地拷贝；用户下载时按节点路由
--   3. generation_task 加 claim_node_id / claim_lease_until 用于 lease 调度
--
-- 设计要点：
--   * download_locator 与 generation_result 解耦：一个 result 可能由多个节点提供下载。
--   * cluster_node 单 PK 用 node_id (VARCHAR(40))，不用自增 ID，便于跨环境 import/export。
--   * hmac_secret_enc 二进制存（AES-256-GCM 密文），运维通过后台 "重新签发" 才能拿明文。
--   * status: 0=待激活 1=启用 2=禁用 9=吊销（吊销不可恢复，需删后重建）。
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS `cluster_node` (
  `node_id`            VARCHAR(40)    NOT NULL                       COMMENT '稳定 id，如 agent-hk-01',
  `display_name`       VARCHAR(120)   NOT NULL DEFAULT ''            COMMENT '展示名',
  `role`               VARCHAR(16)    NOT NULL DEFAULT 'agent'       COMMENT 'control / agent / edge',
  `public_host`        VARCHAR(255)   NOT NULL DEFAULT ''            COMMENT '对外 URL，如 https://hk01.cdn.example',
  `internal_host`      VARCHAR(255)   NOT NULL DEFAULT ''            COMMENT '主控直连地址（可选）',
  `provider_scope`     JSON           NOT NULL                       COMMENT 'JSON array: ["gpt","grok","adobe"]',
  `weight`             INT            NOT NULL DEFAULT 100,
  `max_concurrency`    INT            NOT NULL DEFAULT 16,
  `download_only`      TINYINT        NOT NULL DEFAULT 0             COMMENT '1=只做下载不接任务',
  `allowed_ips`        VARCHAR(512)   NOT NULL DEFAULT ''            COMMENT 'CIDR 列表，逗号分隔；空=不限',
  `hmac_secret_enc`    VARBINARY(255) DEFAULT NULL                   COMMENT 'AES-256-GCM 密文；吊销后置 NULL',
  `bootstrap_used`     TINYINT        NOT NULL DEFAULT 0             COMMENT 'bootstrap token 是否已用',
  `status`             TINYINT        NOT NULL DEFAULT 0             COMMENT '0待激活 1启用 2禁用 3维护中 9吊销',
  `last_heartbeat_at`  DATETIME(3)    DEFAULT NULL,
  `last_inflight`      INT            NOT NULL DEFAULT 0,
  `last_ip`            VARCHAR(45)    NOT NULL DEFAULT '',
  `version`            VARCHAR(60)    NOT NULL DEFAULT '',
  `meta`               JSON           DEFAULT NULL,
  `created_at`         DATETIME(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at`         DATETIME(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`node_id`),
  KEY `idx_status_role` (`status`, `role`),
  KEY `idx_heartbeat`   (`last_heartbeat_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='集群节点注册';
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS `download_locator` (
  `id`           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `asset_kind`   VARCHAR(16)  NOT NULL DEFAULT 'gen'                  COMMENT 'gen=生成结果 / thumb=缩略图 / asset=用户上传',
  `asset_key`    VARCHAR(255) NOT NULL                                COMMENT '通常 = generation_task.task_id + "/" + seq 或 cached 相对路径',
  `node_id`      VARCHAR(40)  NOT NULL,
  `rel_path`     VARCHAR(512) NOT NULL                                COMMENT '相对 storage root 的路径，如 generated/2026/05/14/<uuid>_0.png',
  `size_bytes`   BIGINT       DEFAULT NULL,
  `sha256`       CHAR(64)     DEFAULT NULL,
  `mime`         VARCHAR(120) DEFAULT NULL,
  `status`       TINYINT      NOT NULL DEFAULT 1                      COMMENT '0=失效 1=可用 2=校验失败',
  `last_served_at` DATETIME(3) DEFAULT NULL,
  `served_count` BIGINT       NOT NULL DEFAULT 0,
  `created_at`   DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `expires_at`   DATETIME(3)  DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_kind_key_node` (`asset_kind`, `asset_key`, `node_id`),
  KEY `idx_node_status` (`node_id`, `status`),
  KEY `idx_asset_status` (`asset_kind`, `asset_key`, `status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='资源在哪些节点上有本地拷贝';
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE `generation_task`
  ADD COLUMN `claim_node_id`     VARCHAR(40)   DEFAULT NULL AFTER `account_id`,
  ADD COLUMN `claim_lease_until` DATETIME(3)   DEFAULT NULL AFTER `claim_node_id`,
  ADD KEY `idx_claim` (`claim_node_id`, `status`, `claim_lease_until`);
-- +goose StatementEnd

-- +goose StatementBegin
-- 主控自身作为 embedded agent 自动注册一行；status=1 直接启用，secret 为 NULL（同进程无需 HMAC）。
INSERT INTO `cluster_node`
  (`node_id`, `display_name`, `role`, `public_host`, `provider_scope`, `weight`, `max_concurrency`,
   `download_only`, `hmac_secret_enc`, `status`, `meta`)
VALUES
  ('control-main', '主控（embedded）', 'control', '', JSON_ARRAY('gpt','grok','adobe','pic2api'),
   100, 32, 0, NULL, 1, JSON_OBJECT('embedded', true))
ON DUPLICATE KEY UPDATE updated_at = CURRENT_TIMESTAMP(3);
-- +goose StatementEnd

-- +goose StatementBegin
-- 集群相关 system_config 默认值（system_config 表结构：key/value/remark）
INSERT INTO `system_config` (`key`, `value`, `remark`)
VALUES
  ('cluster.enabled',           JSON_OBJECT('enabled', false), '集群模式总开关，关闭后所有用户下载走主控本地'),
  ('cluster.ticket_ttl_sec',    JSON_OBJECT('seconds', 300),   '下载 ticket 过期时间，秒'),
  ('cluster.heartbeat_dead_sec',JSON_OBJECT('seconds', 90),    '节点心跳静默超过该值视为掉线'),
  ('cluster.lease_ttl_sec',     JSON_OBJECT('seconds', 300),   '任务 lease 默认有效期，秒；超过未上报视为节点死亡')
ON DUPLICATE KEY UPDATE `remark` = VALUES(`remark`);
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS `download_locator`;
DROP TABLE IF EXISTS `cluster_node`;

ALTER TABLE `generation_task`
  DROP KEY `idx_claim`,
  DROP COLUMN `claim_lease_until`,
  DROP COLUMN `claim_node_id`;

DELETE FROM `system_config` WHERE `key` IN
  ('cluster.enabled','cluster.ticket_ttl_sec','cluster.heartbeat_dead_sec','cluster.lease_ttl_sec');
