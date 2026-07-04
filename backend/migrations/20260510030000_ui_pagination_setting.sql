-- 后台 UI 偏好 - 分页相关设置。
--
-- 全局默认每页条数 + Pager 下拉的候选条数。所有列表页（用户 / 号池 /
-- 日志 / 订单 / CDK / 上游 / 邮箱池等）都读这个 key 决定首次加载的
-- 默认值；用户在每个页面右下角的 "每页 N 条" 下拉里临时切换的值会
-- 持久化到浏览器 localStorage（key = `ui.pageSize.session`）。
--
-- 管理员在 "系统配置 → 界面偏好 - 分页" 中可改默认值和候选项。
-- +goose Up
-- +goose StatementBegin
INSERT INTO `system_config` (`key`, `value`, `remark`)
VALUES (
  'ui.pagination',
  JSON_OBJECT(
    'default_page_size', 10,
    'page_size_options', JSON_ARRAY(10, 20, 50, 100, 200, 500, 1000)
  ),
  '后台 UI 偏好 - 分页（默认每页条数 + 候选下拉）'
)
ON DUPLICATE KEY UPDATE `remark` = VALUES(`remark`);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM `system_config` WHERE `key` = 'ui.pagination';
-- +goose StatementEnd
