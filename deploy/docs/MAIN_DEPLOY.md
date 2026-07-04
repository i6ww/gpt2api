# 主控部署 · Control-Plane

> 配套：`CLUSTER_OVERVIEW.md`（架构总览）、`AGENT_DEPLOY.md`（边缘节点）、`LOCAL_CLUSTER.md`（本地联调）。
> 主控 = 用户、计费、账号池、管理后台、调度的唯一权威。**只能有一个**。
> 单机部署时主控自带一个 embedded agent，行为退化为今天的单容器形态，**0 配置 0 改动**。

---

## 1. 资源建议

| 用户量 | CPU | 内存 | 磁盘 | 带宽 | 说明 |
|--------|------|------|------|------|------|
| ≤ 1k DAU | 4c | 8 GB | 100 GB SSD | 50 Mbps | 主控自带 agent 足够 |
| 1k – 5k DAU | 8c | 16 GB | 200 GB SSD | 100 Mbps | 主控 + 1–2 边缘 |
| 5k – 20k DAU | 16c | 32 GB | 400 GB SSD（**只放数据库**） | 200 Mbps | 主控 + ≥ 3 边缘，**主控关掉 embedded agent** |
| 20k+ DAU | 上 K8s | - | - | - | 参考 `docs/06-部署与运维规范.md` §9 |

> 关键：**当边缘 ≥ 3 时，主控应关闭 embedded agent（`KLEIN_EMBEDDED_AGENT=0`），让主控只做调度，IO/CPU 全部腾给 MySQL/Redis**。

---

## 2. 域名与证书规划

| 域名 | 用途 | 端口 | 证书 |
|------|------|------|------|
| `klein.example` | 用户端 | 443→17080 | LE / 阿里 DV |
| `admin.klein.example` | 管理后台 | 443→17088 | LE，**叠加 IP 白名单 + WAF** |
| `api.klein.example` | OpenAI 兼容 | 443→17200 | LE |
| `hk01.cdn.klein.example` | 边缘节点 1（独立机房） | 443→27080 | 在该边缘机器上签 |
| `sg02.cdn.klein.example` | 边缘节点 2 | 443→27080 | 同上 |
| `*.cdn.klein.example` | 通配（可选） | 443→27080 | 单证书覆盖所有节点 |

DNS 上：
- `klein.example` / `admin.klein.example` / `api.klein.example` → 主控公网 IP
- `hk01.cdn` → 边缘 1 公网 IP（A 记录）
- `sg02.cdn` → 边缘 2 公网 IP

**不要** 把 `*.cdn.klein.example` 解析到主控；保持每个子域名直连边缘 IP，主控仅在 302 时拼出该 URL。

---

## 3. 一次性准备

### 3.1 创建系统用户 / 目录

```bash
sudo useradd -r -s /bin/bash -m -d /opt/klein klein || true
sudo mkdir -p /opt/klein/{deploy,storage,backups,logs,env}
sudo chown -R klein:klein /opt/klein
```

### 3.2 拉代码

```bash
sudo -u klein bash <<'EOF'
cd /opt/klein
git clone https://github.com/<org>/gpt2api.git src
cd src
git checkout main
EOF
```

### 3.3 生成机密

> 全部走 `openssl rand -hex 32`，**不要** 用任何密码生成器记忆短串。

```bash
KLEIN_JWT_SECRET=$(openssl rand -hex 32)
KLEIN_JWT_REFRESH_SECRET=$(openssl rand -hex 32)
KLEIN_AES_KEY=$(openssl rand -hex 32)
KLEIN_CLUSTER_BOOTSTRAP_SECRET=$(openssl rand -hex 32)   # 用于签发 agent handshake token
KLEIN_DOWNLOAD_TICKET_SECRET=$(openssl rand -hex 32)     # 用于额外保护 ticket（与 node_secret 双层）
KLEIN_MYSQL_ROOT_PASSWORD=$(openssl rand -hex 20)
KLEIN_MYSQL_PASSWORD=$(openssl rand -hex 20)
```

把上面这些写进 `/opt/klein/env/.env.prod`（**绝不入仓**）：

```ini
KLEIN_ENV=prod
KLEIN_NODE_ID=control-main
KLEIN_NODE_ROLE=control                # control / agent / edge
KLEIN_CLUSTER_ENABLED=1                # 集群模式开关，单机部署可设为 0
KLEIN_EMBEDDED_AGENT=1                 # 主控自带 agent；边缘≥3 时改 0

# 公开根 URL（用于 302 时拼绝对地址，可选）
KLEIN_PUBLIC_BASE_URL=https://klein.example

# 端口
KLEIN_USER_WEB_PORT=17080
KLEIN_ADMIN_WEB_PORT=17088
KLEIN_OPENAI_PORT=17200
KLEIN_API_PORT=17180
KLEIN_ADMIN_PORT=17188

# DB / Redis
KLEIN_MYSQL_PORT=13306
KLEIN_MYSQL_DB=klein_ai
KLEIN_MYSQL_USER=klein
KLEIN_MYSQL_ROOT_PASSWORD=<...>
KLEIN_MYSQL_PASSWORD=<...>
KLEIN_DB_DSN=klein:<KLEIN_MYSQL_PASSWORD>@tcp(mysql:3306)/klein_ai?charset=utf8mb4&parseTime=True&loc=Local

KLEIN_REDIS_PORT=16379
KLEIN_REDIS_ADDR=redis:6379
KLEIN_REDIS_PASSWORD=

# 安全
KLEIN_JWT_SECRET=<...>
KLEIN_JWT_REFRESH_SECRET=<...>
KLEIN_AES_KEY=<...>
KLEIN_CLUSTER_BOOTSTRAP_SECRET=<...>
KLEIN_DOWNLOAD_TICKET_SECRET=<...>

# Provider
KLEIN_PROVIDER_GPT=real
KLEIN_PROVIDER_GROK=real
KLEIN_PROVIDER_ADOBE=real
KLEIN_PROVIDER_PIC2API=real

# 跨域
KLEIN_CORS_ORIGINS=https://klein.example,https://admin.klein.example
```

> ⚠️ 把 `/opt/klein/env/.env.prod` 权限改 `chmod 600`，并把 `KLEIN_AES_KEY` / `KLEIN_CLUSTER_BOOTSTRAP_SECRET` 另存一份到 KMS / Bitwarden / 1Password，**丢了密文谁也救不了**。

---

## 4. 启动

### 4.1 启动 compose

```bash
cd /opt/klein/src/deploy
docker compose --env-file /opt/klein/env/.env.prod up -d --build
```

首次启动会：
- 拉 mysql:8.0 / redis:7-alpine 镜像；
- 构建 `kleinai/backend:latest` 与 `kleinai/user-web:latest`、`kleinai/admin-web:latest`；
- mysql 初始化时自动 `goose up` 全部 migrations（含本次新增的 `cluster_node` / `download_locator`）；
- nginx 用 `deploy/nginx/{user,admin,openai}.conf` 反代。

### 4.2 健康检查

```bash
curl -fsS http://127.0.0.1:17080/api/v1/healthz
curl -fsS http://127.0.0.1:17188/admin/api/v1/healthz
curl -fsS http://127.0.0.1:17200/v1/models -H 'Authorization: Bearer <user-key>'
```

正常返回 `{"ok":true}`。

### 4.3 首次登录管理后台

- URL: `https://admin.klein.example/`（或 `http://<server>:17088/` 本地）
- 默认账号：`admin` / `admin123` —— **登录后立刻改密** + 开启 TOTP。

---

## 5. 集群相关后台动作（每次扩容必做）

> 入口：管理后台 → 系统 → **集群节点**（在「代理管理」下方，本次新增）。

### 5.1 注册一个边缘节点

| 字段 | 说明 | 示例 |
|------|------|------|
| `node_id` | 全局唯一，建议 `agent-<区域>-<序号>` | `agent-hk-01` |
| `display_name` | 展示用 | 香港 BGP 1 |
| `role` | `agent` / `edge`（`edge`=只下载不跑任务） | `agent` |
| `public_host` | 边缘节点对外 URL | `https://hk01.cdn.klein.example` |
| `provider_scope` | 该节点跑哪些 provider，多选 | `gpt,grok,adobe` |
| `weight` | 调度权重（越高分到越多） | 100 |
| `max_concurrency` | 单节点最大 inflight | 16 |
| `download_only` | 只做下载、不跑任务 | 否 |
| `allowed_ips` | 允许的 agent 出口 IP（CIDR，多个用逗号） | `203.0.113.42/32` |

提交后系统：
1. 在 `cluster_node` 表写一行 `status=0`（待激活）；
2. 生成 `bootstrap_token = base64(node_id + sign(KLEIN_CLUSTER_BOOTSTRAP_SECRET, node_id+nonce))`；
3. **页面只展示一次**：`KLEIN_NODE_ID` / `KLEIN_NODE_TOKEN` / `KLEIN_CONTROL_URL`，运维把这 3 个值贴到边缘节点的 `.env`（详见 `AGENT_DEPLOY.md`）。

### 5.2 节点状态

| 状态 | 含义 |
|------|------|
| `待激活` | 已创建未握手 |
| `在线` | 心跳 ≤ 90s |
| `掉线` | 心跳 90s – 10 min |
| `失联` | 心跳 > 10 min（自动从调度池剔出） |
| `禁用` | 运维手动停用（不再分配任务，已分配的等到 lease 到期被回收） |
| `吊销` | secret 已失效，agent 无法访问任何 `/cluster/*`（用于节点被入侵后的紧急止血） |

### 5.3 主控自身的 agent

主控启动后会自动注册一行：
- `node_id = control-main`
- `role = control`
- `public_host` 留空（不被路由出去）
- `provider_scope = gpt,grok,adobe`（与现有 worker 行为一致）

如果你要让主控**只调度不跑任务**，去后台把它改成 `禁用` 或干脆改 `KLEIN_EMBEDDED_AGENT=0` 重启。

---

## 6. nginx & TLS

主控 nginx 现状（`deploy/nginx/user.conf`、`admin.conf`、`openai.conf`）已经能用，集群上线后增量改动：

### 6.1 用户端入口：保留 302 长链路

```nginx
# user.conf 增量
server {
  listen 17080 ssl http2;
  server_name klein.example;

  # 关键：cached 资源不要 proxy_buffering 死，让 302 立即吐回
  location /api/v1/gen/cached/ {
    proxy_pass http://klein_api;
    proxy_http_version 1.1;
    proxy_buffering off;
    proxy_redirect off;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    # 用户端在 302 时直接跟到边缘子域名
  }

  location /api/ {
    proxy_pass http://klein_api;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_read_timeout  300s;
  }

  location /v1/ {
    proxy_pass http://klein_openai:17200;
    proxy_read_timeout 600s;     # 视频长连接
  }

  location / {
    proxy_pass http://user-web;
  }
}
```

### 6.2 CORS for cross-domain assets

**重要**：用户站 `klein.example` 跳到 `hk01.cdn.klein.example` 下载，浏览器会发跨域请求。
- 普通 `<img src>` / `<video src>` 不受影响；
- Canvas / fetch 取图需要 CORS 头 —— 在边缘 nginx 上加 `Access-Control-Allow-Origin: https://klein.example`（见 `AGENT_DEPLOY.md` §4）。

---

## 7. 备份策略（保持不变）

| 对象 | 频率 | 介质 | 保留 |
|------|------|------|------|
| MySQL 全量 | 每日 02:00 | S3 / 本地 + 异机房 | 30 天 |
| MySQL binlog | 实时 | 备库 + S3 | 7 天 |
| Redis RDB | 每日 | 本地 | 7 天 |
| `klein-storage` 卷 | **不备份** | - | - |
| 边缘节点磁盘 | **不备份** | - | - |

**生成结果不进备份**：丢了重新跑就行。如果业务上有"图片必须永久保留"需求，单独跑一个`asset-archiver` worker 把 ≥ 1 周的资源转到 OSS（现有 `uploadCachedAssetToOSS` 已可复用）。

---

## 8. 主控滚动更新

```bash
cd /opt/klein/src
sudo -u klein git pull --ff-only
cd deploy
docker compose --env-file /opt/klein/env/.env.prod build api admin openai worker
docker compose --env-file /opt/klein/env/.env.prod up -d --no-deps api admin openai worker
docker compose exec nginx nginx -s reload
```

**注意**：升主控版本时 `cluster_node` schema 可能变更。先看 `backend/migrations/` 增量，必要时手动 `goose up`：

```bash
docker run --rm --network klein-net \
  -v $(pwd)/../backend/migrations:/migrations \
  ghcr.io/pressly/goose:latest \
  -dir /migrations mysql "klein:<password>@tcp(mysql:3306)/klein_ai?parseTime=true" up
```

---

## 9. 紧急操作

### 9.1 一键吊销节点

```bash
# 后台 → 集群节点 → 点 [吊销]
# 等价于 SQL:
UPDATE cluster_node
   SET status = 9, hmac_secret_enc = NULL, updated_at = NOW(3)
 WHERE node_id = 'agent-hk-01';
```

吊销 5 秒内：
- 该节点所有 `/cluster/*` 请求返回 401；
- 用户态点击图片：主控查 locator 时跳过该节点，自动 fallback；
- agent 下次心跳收到 401 → 自动停消费并尝试重新 handshake（拿不到，挂起等待运维）。

### 9.2 暂停所有调度

```bash
# 后台 → 系统配置 → 集群开关 → 关闭
# 等价于：
UPDATE system_config SET val_json = JSON_SET(val_json,'$.enabled', false) WHERE id='cluster.enabled';
```

主控 `/cluster/lease` 立即返回空数组，任务全部堆在 `status=0`。

### 9.3 切回单机

```bash
# 1) 把所有 agent 节点 禁用 或 吊销
# 2) 把 control-main 改成 embedded agent
docker compose exec api sh -c 'curl -X POST localhost:17188/admin/api/v1/system/cluster/embedded?on=1 -H "X-Admin-Token: <...>"'
# 3) 旧链接（已经 302 到边缘的）会失效；新链接走主控本地 c.File()
```

---

## 10. 与单机版兼容性矩阵

| 场景 | 集群关 (`KLEIN_CLUSTER_ENABLED=0`) | 集群开 + 仅 control-main | 集群开 + 多 agent |
|------|------------------------------------|--------------------------|-------------------|
| 用户访问 `/gen/cached/x` | `c.File()` 直接吐 | 同上（无 locator → 兜底） | 302 到边缘 |
| 任务调度 | 现有 inline / asynq | embedded agent inline | lease 拉到对应 node |
| 凭证安全 | 现状 | 现状 | one-shot AES 重封 |
| 数据库 schema | 现状 | 多 `cluster_node`/`download_locator` 表（空） | 同左 |
| 单测 / e2e | ✅ 全跑 | ✅ 全跑 | ✅ 需带 agent 起 |

**结论**：升级到本架构对**未启用集群的现有部署 0 行为变更**，可以放心生产升级再决定何时开集群。
