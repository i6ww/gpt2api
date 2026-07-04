# gpt2api / KleinAI

> **最新版本：`v3.0.1`（多平台多通道聚合，全面更新）** —— 详见 [CHANGELOG](CHANGELOG.md)

> 一个面向多平台账号池 + 第三方上游 API 的 AIGC 聚合平台，提供图片、文字、视频、音乐的一站式生成能力。  
> 前台面向创作，后台面向运营，开放 OpenAI / Gemini 兼容接口，方便直接接入现有 SDK。

## 3.0 是什么

`v3.0.1` 在 2.x 的基础上，把「单一账号池」升级成「多平台、多通道、可路由、可计费」的聚合调度层：

- **多上游接入**：GPT、GROK（Web）、官方 xAI（api.x.ai）、Adobe Firefly、pic2api、FlowMusic 全部打通
- **同一平台多通道**：一个平台可以同时挂多条通道（例如 GPT 有 Web 与官方 API 两条、GROK 有 Web 图像/视频与官方 xAI 两条），按优先级 / 成本 / 可用性自动路由
- **上游 API 管理**：除了本地号池，还能直接填 `base_url + api_key` 接入任意 OpenAI 兼容的第三方付费 API，作为通道纳入统一调度与计费
- **模型路由表**：内部 `model_code`（含尺寸 / 时长 variant）→ 通道，支持优先级、成本倍率、启用开关，运营在后台可视化配置
- **上游计费与利润**：每条通道独立计费模式（按次 / 按量 / 按 token / 按 credit / 订阅平摊 / 自定义），自动记成本日志、出利润报表
- **集群调度**：多节点注册（`cluster_node`），按 provider 维度分配并发与作用域，主控内置 embedded agent
- **统一生成链路**：文字、图片、视频、音乐四条链路统一账号池、代理、刷新、熔断、轮换、用量检测
- **统一兼容 API**：OpenAI 兼容 + Gemini（`/v1beta`）兼容，前端和第三方 SDK 都能直接接
- **多语言前端**：用户前台内置 i18n
- 统一部署方式：单机 Docker Compose 可跑，后续可平滑迁移到 K8s

## 界面预览

### 用户创作端

![gpt2api 用户创作端](docs/ffaa6c7c-ee8b-4a1f-bdcc-df93c8d91abf.png)

### 管理后台

![gpt2api 管理后台](docs/7f76a99c-1100-4216-970b-624ff135808d.png)

## 支持的上游与通道

| 平台 | 通道 | 说明 |
| --- | --- | --- |
| GPT | Web 通道（chatgpt.com）| 号池摊销，走网页链路出图 / 对话 |
| GPT | 官方 API 通道（api.openai.com）| 官方按量计费，图片 / chat |
| GROK | Grok Web 图像 / 视频（grok.com）| 号池摊销，图片与 5/10/20/30 秒视频 |
| GROK | 官方 xAI API（api.x.ai）| OAuth 令牌，视频生成与额度查询 |
| Adobe | Firefly 1K / 2K / 4K（credit 分档）| 按 credit 计费，也支持 CC Premium 订阅平摊 |
| pic2api | OpenAI 兼容外部 API | nano-banana / gpt-image-2 / gemini 图片等 |
| FlowMusic | 音乐生成 | 文本生成歌曲 |
| 自定义 | 任意 OpenAI 兼容第三方 API | 后台填 `base_url + api_key` 即可作为通道纳入调度 |

> 同一个平台可以同时启用多条通道；系统按「模型路由表 + 优先级 + 成本倍率 + 可用性」自动选择通道，单条通道熔断时会自动切到下一条。

## 核心能力

- 多平台账号池：GPT / GROK / xAI / Adobe / pic2api / FlowMusic 账号批量导入、刷新、检测、熔断、轮换、按号并发控制
- 上游 API 管理：本地号池通道（`local_pool`）与外部直连通道（`external_api`）统一管理，支持每通道独立计费与模型路由
- 创作中心：文字对话、文生图、图生图、文生视频、图生视频、音乐生成
- 异步任务：图片 / 视频 / 音乐支持任务查询、历史记录、预览、下载
- OpenAI 兼容 API：
  - `GET /v1/models`
  - `POST /v1/chat/completions`
  - `POST /v1/images/generations` · `POST /v1/images/edits` · `GET /v1/images/generations/:task_id`
  - `POST /v1/video/generations` · `GET /v1/video/generations/:task_id`（`/v1/videos/...` 亦兼容）
  - `POST /v1/music/generations` · `GET /v1/music/generations/:task_id`
- Gemini 兼容 API：
  - `GET /v1beta/models`
  - `POST /v1beta/models/:model:generateContent`
- 集群调度：多节点注册、按 provider 划分并发与作用域、主控内置 embedded agent
- 管理后台：仪表盘、账号 / Token 管理、上游通道与路由、代理管理、用户管理、充值消费、优惠码、CDK、系统配置、模型价格、请求日志、上游日志、利润报表
- 运营能力：充值套餐、扣费规则、模型映射、自动刷新、上游成本 / 利润追踪
- 部署能力：支持本地开发、单机部署、集群扩容、反向代理、SSL 证书自动更新

## 设计目标

1. 前台简洁统一，文字 / 图片 / 视频 / 音乐入口标准化，并支持多语言
2. 后台更适合运营，账号、通道、路由、价格尽量表单化，不依赖 JSON 手填
3. 上游多元：同一模型可挂多平台多通道，按优先级 / 成本 / 可用性自动路由与降级
4. 账号与请求逻辑稳定，支持自动重试、换号、熔断、分批刷新、按号并发控制
5. 成本可算、上游可追踪，失败时能看到完整 provider 日志，并能出利润报表
6. 部署简单，单机 Linux 能直接跑起来，需要时可按 provider 维度做集群扩容

## 技术栈

- 后端：Go 1.24 + Gin + GORM + MySQL + Redis
- 前端：React 18 + Vite + TypeScript + Tailwind + i18n
- 部署：Docker / Docker Compose / Nginx / Caddy（单机或多节点集群）
- 外部依赖：FlareSolverr、代理池、对象存储（可选）、各上游平台 API

## 仓库结构

```text
.
├── backend/     # Go 后端：API / Admin / OpenAI 兼容 / Worker
├── frontend/    # 用户前台 + 管理后台
├── deploy/      # Docker Compose、Nginx、Caddy、环境变量
├── docs/        # 开发、API、部署、前端规范
└── README.md
```

## 端口说明

### 对外端口

- `17080`：用户前台
- `17088`：管理后台
- `17200`：OpenAI 兼容 API

### 本机调试端口

- `17180`：用户后端 API
- `17188`：管理后台 API
- `17200`：OpenAI 兼容 API
- `23306`：MySQL
- `16379`：Redis
- `18191`：FlareSolverr

## 快速部署

下面是推荐的线上部署方式，和当前仓库的 `deploy/docker-compose.server.yml` 对齐。

### 1. 准备环境

- 一台 Linux 服务器
- Docker 和 Docker Compose
- 1 个域名或 3 个子域名
- 80 / 443 端口可用
- MySQL / Redis 空间充足

### 2. 拉取代码

```bash
git clone https://github.com/432539/gpt2api.git
cd gpt2api
```

### 3. 配置环境

复制环境变量模板并修改：

```bash
cp deploy/env/.env.example deploy/env/.env.prod
```

重点检查这些项：

- 数据库连接
- Redis 地址
- JWT 密钥
- AES 密钥
- 域名 / CORS
- 各上游基础地址（OpenAI / GROK / xAI / Adobe / pic2api / FlowMusic）
- 代理与 FlareSolverr 地址
- 上游接入方式：本地号池账号在后台「账号管理」导入；第三方 OpenAI 兼容 API 在「上游通道」填 `base_url + api_key`

### 4. 启动服务

```bash
cd deploy
docker compose -f docker-compose.server.yml up -d --build
```

### 5. 检查状态

```bash
docker compose -f docker-compose.server.yml ps
docker logs -f klein-api-dev
docker logs -f klein-admin-dev
docker logs -f klein-openai-dev
docker logs -f klein-worker-dev
```

### 6. 访问地址

- 用户前台：`http(s)://你的域名:17080`
- 管理后台：`http(s)://你的域名:17088`
- OpenAI 兼容 API：`http(s)://你的域名:17200/v1`

## 生产建议

- 前台、后台、API 分域名部署更清晰
- 管理后台建议限制来源 IP
- OpenAI 兼容接口建议走独立域名
- 80 / 443 端口建议由 Caddy 或 Nginx 统一接管 SSL
- 图片和视频素材建议落 OSS 或本地缓存，避免直接暴露上游地址

## 开发方式

```bash
cd deploy
docker compose -f docker-compose.dev-full.yml up -d --build
```

本地开发时，前后端都可以单独启动，也可以只拉起 MySQL / Redis。

## 文档

- [更新日志 CHANGELOG](CHANGELOG.md)
- [开发规范](docs/01-开发规范-总览.md)
- [后端规范](docs/02-后端规范.md)
- [数据库设计](docs/03-数据库设计.md)
- [API 规范](docs/04-API规范.md)
- [前端规范](docs/05-前端规范.md)
- [部署与运维规范](docs/06-部署与运维规范.md)

## 开源地址

- [https://github.com/432539/gpt2api](https://github.com/432539/gpt2api)

## 版本与历史

- 当前默认版本：`v3.0.1`
- `v3.0.1`：多平台多通道聚合（GPT / GROK / xAI / Adobe / pic2api / FlowMusic）、上游 API 管理与模型路由、上游计费与利润报表、集群调度、音乐生成、Gemini 兼容接口、前端多语言
- `v2.0.x`：统一文字 / 图片 / 视频生成链路与后台运营能力
- `v1.0.x`：保留为历史稳定版本，可通过 Git tag / 分支继续查看
- 后续发布建议采用 `v3.0.x` 继续演进，避免覆盖旧版说明

## Stars

[![GitHub stars](https://img.shields.io/github/stars/432539/gpt2api?style=flat-square)](https://github.com/432539/gpt2api)
