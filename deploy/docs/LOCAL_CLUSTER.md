# 本地多端口集群联调

> 配套：`CLUSTER_OVERVIEW.md`、`MAIN_DEPLOY.md`、`AGENT_DEPLOY.md`。
> 目标：**一台开发机** 同时跑 1 个主控 + N 个边缘 agent，全部容器化，端口不冲突，能在浏览器里复现"用户点图 → 302 → 边缘下载"的真实链路。

---

## 1. 端口分配（与生产对齐 + 边缘叠加偏移）

| Stack | 服务 | 容器内端口 | 主机暴露 |
|-------|------|------------|----------|
| **control-local** | user-web | 17080 | 17080 |
| | admin-web | 17088 | 17088 |
| | openai | 17200 | 17200 |
| | api（直连） | 17180 | 17180 |
| | admin（直连） | 17188 | 17188 |
| | mysql | 3306 | 13306 |
| | redis | 6379 | 16379 |
| **agent-1-local** | agent | 27180 | 27180 |
| | edge-nginx | 27080 | 27080 |
| **agent-2-local** | agent | 27180 | 28180 |
| | edge-nginx | 27080 | 28080 |
| **agent-3-local** | agent | 27180 | 29180 |
| | edge-nginx | 27080 | 29080 |

> 命名约定：第 N 个 agent 的主机端口 = 「容器端口 + 1000×(N-1)」。

`/etc/hosts`（Windows: `C:\Windows\System32\drivers\etc\hosts`）追加：

```
127.0.0.1   klein.local
127.0.0.1   admin.klein.local
127.0.0.1   api.klein.local
127.0.0.1   hk01.cdn.klein.local
127.0.0.1   sg02.cdn.klein.local
127.0.0.1   us03.cdn.klein.local
```

---

## 2. 主控（control-local）

直接复用现有 `deploy/docker-compose.yml`，仅环境变量略改：

```bash
cd deploy
cp env/.env.example env/.env.local
# 修改 env/.env.local：
#   KLEIN_CLUSTER_ENABLED=1
#   KLEIN_EMBEDDED_AGENT=1
#   KLEIN_PUBLIC_BASE_URL=http://klein.local:17080
#   KLEIN_CLUSTER_BOOTSTRAP_SECRET=$(openssl rand -hex 32)
#   KLEIN_DOWNLOAD_TICKET_SECRET=$(openssl rand -hex 32)
```

启动：

```powershell
docker compose --env-file env\.env.local up -d --build
```

健康检查：

```powershell
curl http://127.0.0.1:17180/healthz
curl http://127.0.0.1:17188/admin/api/v1/healthz
```

登录管理后台 `http://admin.klein.local:17088/` → `admin` / `admin123`。

---

## 3. 边缘 agent（agent-1-local / agent-2-local / agent-3-local）

### 3.1 注册节点 + 拿 bootstrap token

后台 → **集群节点** → 添加：

| 字段 | agent-1 | agent-2 | agent-3 |
|------|---------|---------|---------|
| node_id | `agent-1-local` | `agent-2-local` | `agent-3-local` |
| public_host | `http://hk01.cdn.klein.local:27080` | `http://sg02.cdn.klein.local:28080` | `http://us03.cdn.klein.local:29080` |
| provider_scope | `gpt,grok,adobe` | `gpt,adobe` | `grok`（只跑视频） |
| weight | 100 | 50 | 100 |
| max_concurrency | 4 | 4 | 4 |
| allowed_ips | `0.0.0.0/0`（本地放开） | 同 | 同 |

每个节点 **创建时只显示一次** 三件套，写到对应 `.env`。

### 3.2 用同一个 compose 启 3 份 stack

`deploy/docker-compose.cluster-local.yml`（本次新增，下文给出完整内容）会通过 `COMPOSE_PROJECT_NAME` + `KLEIN_NODE_INDEX` 起多份。

启动每一份：

```powershell
# agent-1-local
docker compose -f deploy/docker-compose.cluster-local.yml `
  --env-file deploy/env/.env.agent-1.local `
  -p klein-agent-1 up -d

# agent-2-local
docker compose -f deploy/docker-compose.cluster-local.yml `
  --env-file deploy/env/.env.agent-2.local `
  -p klein-agent-2 up -d

# agent-3-local
docker compose -f deploy/docker-compose.cluster-local.yml `
  --env-file deploy/env/.env.agent-3.local `
  -p klein-agent-3 up -d
```

样例 `deploy/env/.env.agent-1.local`：

```ini
KLEIN_NODE_ID=agent-1-local
KLEIN_NODE_TOKEN=<后台拷过来>
KLEIN_CONTROL_URL=http://host.docker.internal:17188
KLEIN_NODE_PUBLIC_URL=http://hk01.cdn.klein.local:27080
KLEIN_AGENT_PORT=27180
KLEIN_EDGE_HTTP_PORT=27080
KLEIN_AGENT_HOST_PORT=27180          # 主机映射
KLEIN_EDGE_HOST_PORT=27080
KLEIN_NODE_INDEX=1
```

样例 `.env.agent-2.local`：

```ini
KLEIN_NODE_ID=agent-2-local
KLEIN_NODE_TOKEN=<...>
KLEIN_CONTROL_URL=http://host.docker.internal:17188
KLEIN_NODE_PUBLIC_URL=http://sg02.cdn.klein.local:28080
KLEIN_AGENT_PORT=27180
KLEIN_EDGE_HTTP_PORT=27080
KLEIN_AGENT_HOST_PORT=28180
KLEIN_EDGE_HOST_PORT=28080
KLEIN_NODE_INDEX=2
```

> Windows 上 `host.docker.internal` 自带可用；Linux 需要在 compose 里加 `extra_hosts: ["host.docker.internal:host-gateway"]`，本次 compose 已经加好。

### 3.3 验证

```powershell
# 看后台
curl http://127.0.0.1:17188/admin/api/v1/cluster/nodes `
  -H "Authorization: Bearer <admin token>" | jq

# 看 agent 1
docker logs klein-agent-1-agent-1 --tail 20

# 看 nginx 1
curl http://hk01.cdn.klein.local:27080/healthz
```

3 个节点应该都显示 **在线**。

---

## 4. 端到端验证矩阵

```powershell
# 1. 用户端登录拿 token
$token = (Invoke-RestMethod -Method Post -Uri http://klein.local:17080/api/v1/auth/login `
  -ContentType application/json `
  -Body '{"username":"u1","password":"p1"}').data.access_token

# 2. 创建一张图
$task = Invoke-RestMethod -Method Post -Uri http://klein.local:17080/api/v1/gen/image `
  -Headers @{ Authorization="Bearer $token" } `
  -ContentType application/json `
  -Body '{"model_code":"nano-banana","prompt":"a klein blue cat","ratio":"1:1"}'
$taskId = $task.data.task_id

# 3. 轮询直到 status=2
while ((Invoke-RestMethod "http://klein.local:17080/api/v1/gen/task/$taskId" `
   -Headers @{ Authorization="Bearer $token" }).data.status -lt 2) {
  Start-Sleep -Seconds 2
}

# 4. 看哪个 node 接走的
docker exec klein-mysql mysql -uroot -p"$env:KLEIN_MYSQL_ROOT_PASSWORD" -e `
  "SELECT claim_node_id, status FROM klein_ai.generation_task WHERE task_id='$taskId';"

# 5. 看 download_locator
docker exec klein-mysql mysql -uroot -p"$env:KLEIN_MYSQL_ROOT_PASSWORD" -e `
  "SELECT asset_key, node_id, rel_path FROM klein_ai.download_locator WHERE asset_key LIKE '$taskId%';"

# 6. 直接拉用户态资源，跟踪 302
curl -v http://klein.local:17080/api/v1/gen/cached/generated/2026/05/14/<...>.png 2>&1 `
  | Select-String "Location:|HTTP/"
# 应该看到 302 → http://hk01.cdn.klein.local:27080/d/eyJ... → 200
```

---

## 5. 故障注入演练

### 5.1 关掉一个 agent，看用户端是否自动切

```powershell
docker compose -f deploy\docker-compose.cluster-local.yml -p klein-agent-1 stop
# 等 90s 主控就把它标 掉线（或运维直接到后台改 维护中）
# 重新点开同一张图：用户态依然能下载，主控查 locator 时发现 agent-1 失联，
# 如果该 asset_key 还有其他节点持有 → 302 到其他 node，否则回退到主控本地
```

### 5.2 模拟磁盘炸

```powershell
docker exec klein-agent-1-agent-1 rm -rf /var/klein/storage/public/generated
# 用户态访问该资源：边缘 nginx 返 404 → 主控收到上游 404 后
# 把该 locator status=2，下次跳别的 node 或主控 fallback
```

### 5.3 模拟节点被入侵 → 一键吊销

后台「集群节点」点 **吊销**：
- agent-1 日志立刻：`heartbeat 401 unauthorized` → 自动停消费；
- 用户态访问 → 主控 302 改投 agent-2 或本地。

---

## 6. 多端口本地 = 真实地理分布

> 这种 N 端口 / N 子域名 + `/etc/hosts` 的方式，跟生产上「不同机房 / 不同 IP」拓扑**逻辑完全一致**，只是地址变了。任何在本地能跑通的故障演练，在生产上都能复现。

---

## 7. 常见坑

| 现象 | 原因 | 修 |
|------|------|----|
| agent 起不来：`dial tcp host.docker.internal:17188: i/o timeout` | Linux 没加 host-gateway | compose 里 `extra_hosts: ["host.docker.internal:host-gateway"]`（已默认开） |
| 302 后浏览器没拉到图 | `/etc/hosts` 没加 cdn 子域名 | 见 §1 |
| 302 后 CORS 报错 | edge nginx 没加 `Access-Control-Allow-Origin` | 见 `AGENT_DEPLOY.md` §4.3 |
| 浏览器一直转 `https://hk01.cdn...` | 本地 http，但前端代码强制 https | 改 `KLEIN_NODE_PUBLIC_URL=http://...`；后台同时改 public_host |
| 多个 agent 抢到同一任务 | MySQL 8.0 之前，`SKIP LOCKED` 不支持 | 升 MySQL 8.0+ 或用 Redis Lua 抢锁兜底（已实现） |
| `download_locator` 满了 | 没启用 LRU | `KLEIN_AGENT_STORAGE_QUOTA_GB=80` 设小一点 |

---

## 8. 一键脚本

`scripts/cluster-up.ps1`（本次将新增）：

```powershell
param([int]$AgentCount = 2)

docker compose --env-file deploy/env/.env.local up -d --build
Write-Host "Control plane: http://klein.local:17080"
Start-Sleep -Seconds 5

for ($i = 1; $i -le $AgentCount; $i++) {
  $envFile = "deploy/env/.env.agent-$i.local"
  if (-not (Test-Path $envFile)) {
    Write-Warning "$envFile not found, skip. Run 添加节点 in admin UI first."
    continue
  }
  docker compose -f deploy/docker-compose.cluster-local.yml `
    --env-file $envFile -p "klein-agent-$i" up -d
}

docker ps --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}"
```

`scripts/cluster-down.ps1`：

```powershell
docker compose -p klein-agent-1 down
docker compose -p klein-agent-2 down
docker compose -p klein-agent-3 down
docker compose down
```
