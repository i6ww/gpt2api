-- +goose Up
-- +goose StatementBegin

-- 系统公告：admin 后台维护，对外用户端顶部滚动条展示。
-- 支持时间窗（start_at/end_at 留空表示长期生效）、级别（info/success/warning/danger）、
-- 可点击跳转、置顶、排序。
CREATE TABLE IF NOT EXISTS `announcement` (
  `id`         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `title`      VARCHAR(128)  NOT NULL                              COMMENT '公告标题（必填，列表用）',
  `content`    TEXT          NOT NULL                              COMMENT '公告正文（用户端滚动条/详情展示）',
  `level`      VARCHAR(16)   NOT NULL DEFAULT 'info'              COMMENT 'info / success / warning / danger',
  `link_url`   VARCHAR(500)  DEFAULT NULL                          COMMENT '可选跳转 URL（http/https/相对路径）',
  `link_text`  VARCHAR(64)   DEFAULT NULL                          COMMENT '可选跳转按钮文字，例如「立即查看」',
  `pinned`     TINYINT       NOT NULL DEFAULT 0                    COMMENT '是否置顶：1=置顶到首位，0=按 sort_order 排序',
  `enabled`    TINYINT       NOT NULL DEFAULT 1                    COMMENT '是否启用：0=隐藏 / 1=显示',
  `start_at`   DATETIME(3)   DEFAULT NULL                          COMMENT '生效起始时间（NULL=立即生效）',
  `end_at`     DATETIME(3)   DEFAULT NULL                          COMMENT '生效截止时间（NULL=永久有效）',
  `sort_order` INT           NOT NULL DEFAULT 0                    COMMENT '排序：数字小的靠前；置顶优先',
  `created_by` BIGINT UNSIGNED DEFAULT NULL                        COMMENT '发布的 admin_user.id',
  `created_at` DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3)   DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_announcement_active` (`enabled`, `deleted_at`, `start_at`, `end_at`),
  KEY `idx_announcement_sort` (`pinned`, `sort_order`, `id`),
  KEY `idx_announcement_deleted` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='系统公告（用户端顶部滚动条展示）';

-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS `announcement`;
