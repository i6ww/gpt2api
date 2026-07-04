-- +goose Up
-- +goose StatementBegin

INSERT INTO `system_config` (`key`, `value`, `remark`) VALUES
  ('captcha.provider', '"capsolver"', '验证码 solver: capsolver / yescaptcha / 2captcha'),
  ('captcha.api_key',  '""',           'Solver API Key（必填，否则 Adobe / Grok 注册无法通过人机校验）'),
  ('captcha.endpoint', '""',           '自定义 solver endpoint，留空使用默认（CapSolver 为 https://api.capsolver.com）')
ON DUPLICATE KEY UPDATE `key`=`key`;

-- +goose StatementEnd

-- +goose Down
DELETE FROM `system_config` WHERE `key` IN
  ('captcha.provider', 'captcha.api_key', 'captcha.endpoint');
