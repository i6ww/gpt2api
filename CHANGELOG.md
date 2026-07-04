# 更新日志 / Changelog

本项目采用 `vMAJOR.MINOR.PATCH` 版本号，最新版本见顶部。

---

## v3.0.1 —— 最新版本（多平台多通道聚合）

> 这是当前最新更新，几乎覆盖了平台的所有模块：新增多个上游平台与通道、引入上游 API 管理与模型路由、上游计费与利润报表、集群调度、音乐生成、Gemini 兼容接口，以及前端多语言。

### 上游与通道（重点）

- **多平台接入**：GPT、GROK（Web）、官方 xAI（api.x.ai）、Adobe Firefly、pic2api、FlowMusic 全部打通。
- **同一平台多通道**：
  - GPT：Web 通道（chatgpt.com，号池摊销）+ 官方 API 通道（api.openai.com，按量计费）。
  - GROK：Grok Web 图像 / 视频（grok.com，号池摊销，5/10/20/30 秒视频）+ 官方 xAI API（api.x.ai，OAuth 令牌，视频与额度）。
  - Adobe Firefly：1K / 2K / 4K credit 分档，另支持 CC Premium 订阅平摊。
  - pic2api：OpenAI 兼容外部 API（nano-banana / gpt-image-2 / gemini 图片等）。
  - FlowMusic：文本生成歌曲。
- **上游 API 管理**：本地号池通道（`local_pool`）与外部直连通道（`external_api`）统一管理；外部通道只需填 `base_url + api_key` 即可接入任意 OpenAI 兼容第三方付费 API。
- **模型路由表**：内部 `model_code`（含尺寸 / 时长 variant）→ 通道，支持优先级、成本倍率、启用开关；单条通道熔断自动切下一条。
- **上游计费与利润**：每条通道独立计费模式（按次 / 按量 / 按 token / 按 credit / 订阅平摊 / 自定义），自动记成本日志、出利润报表。

### 生成能力

- 统一文字、图片、视频、**音乐**四条生成链路。
- OpenAI 兼容接口新增音乐：`POST /v1/music/generations`、`GET /v1/music/generations/:task_id`。
- 视频接口同时兼容 `/v1/video/...` 与 `/v1/videos/...`。
- **Gemini 兼容接口**：`GET /v1beta/models`、`POST /v1beta/models/:model:generateContent`。

### 调度与稳定性

- **集群调度**：多节点注册（`cluster_node`），按 provider 维度分配并发与作用域，主控内置 embedded agent。
- 账号池支持按号并发控制（每号 1 并发、全局上限、号池不足排队）。
- 自动重试、换号、熔断、分批刷新、代理轮换；违禁词在提交上游前预校验直接拒绝。
- Adobe Firefly 反爬对齐（桌面 Chrome 指纹 / 头序 / TLS 指纹 / ARP 会话），408 触发代理轮换而非直接熔断账号。

### 后台与运营

- 新增「上游通道」与「模型路由」管理页、集群节点管理页、多语言相关配置。
- 仪表盘、账号 / Token 管理、代理管理、用户管理、充值消费、优惠码、CDK、系统配置、模型价格、请求日志、上游日志、利润报表。

### 前端

- 用户前台内置 **i18n 多语言**。
- 创作中心统一文字 / 图片 / 视频 / 音乐入口。

### 部署

- 提供单机生产栈 `deploy/docker-compose.prod.yml`（Caddy 自动 TLS + 续期）。
- 支持本地开发、单机部署、按 provider 维度集群扩容、反向代理、SSL 证书自动更新。
- 所有密钥走 `.env`，仓库仅保留占位符；隐私 / 运维临时产物均已在 `.gitignore` 中排除。

---

## v2.0.x

- 统一文字 / 图片 / 视频三条生成链路与账号池、代理、刷新、熔断、轮换、用量检测。
- 统一 OpenAI 兼容 API 与后台运营能力（用户、账单、CDK、优惠码、模型价格、请求日志、上游日志）。
- 统一单机 Docker Compose 部署方式。

## v1.0.x

- 初始稳定版本，保留为历史基线，可通过 Git tag / 分支查看。
