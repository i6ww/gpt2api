# 边缘节点部署 · Agent / Edge

> 配套：`CLUSTER_OVERVIEW.md`、`MAIN_DEPLOY.md`、`LOCAL_CLUSTER.md`。
> 一个 agent = 一台机器 + 一个独立子域名。本文档从「全新一台机器」开始讲到「主控后台显示在线」。

---

## 1. 机器要求

| 资源 | 推荐 | 备注 |
|------|------|------|
| CPU | 4c+ | 视频任务建议 8c |
| 内存 | 8 GB+ | 大量并发图片压缩需要 |
| 磁盘 | 100 GB+ NVMe / SSD | 用作下载缓存，**用满会自动淘汰最旧** |
| 带宽 | ≥ 100 Mbps 上行 | 决定该节点能跑多少下载 |
| 公网 IP | 1 个 | 必须能解析子域名指过来 |
| 操作系统 | Ubuntu 22.04 / Debian 12 / Rocky 9 | docker 24+ |

---

## 2. DNS 与证书

### 2.1 一个 agent 对应一个子域名

| 节点 | 子域名 | 解析到 |
|------|--------|--------|
| `agent-hk-01` | `hk01.cdn.klein.example` | 该机器公网 IP |
| `agent-sg-02` | `sg02.cdn.klein.example` | 该机器公网 IP |

### 2.2 申请证书

机器上装 `acme.sh` / `certbot`，跑：

```bash
sudo curl https://get.acme.sh | sh
~/.acme.sh/acme.sh --issue -d hk01.cdn.klein.example --standalone
~/.acme.sh/acme.sh --install-cert -d hk01.cdn.klein.example \
  --key-file       /opt/klein-agent/certs/edge.key \
  --fullchain-file /opt/klein-agent/certs/edge.fullchain \
  --reloadcmd      "docker compose -f /opt/klein-agent/docker-compose.yml exec nginx nginx -s reload"
```

或主控用通配证书 `*.cdn.klein.example` 一签全用，把证书文件用 `scp` 推到各 agent。

---

## 3. 一次性准备

### 3.1 系统用户 / 目录

```bash
sudo useradd -r -s /bin/bash -m -d /opt/klein-agent klein || true
sudo mkdir -p /opt/klein-agent/{deploy,storage,logs,certs,env}
sudo chown -R klein:klein /opt/klein-agent
```

### 3.2 拉代码（只需 agent 子目录 + dockerfile）

```bash
sudo -u klein bash <<'EOF'
cd /opt/klein-agent
git clone https://github.com/<org>/gpt2api.git src
cd src && git checkout main
EOF
```

> 边缘节点必须用与主控**完全相同的 commit**（HMAC payload schema 在版本之间可能变）。生产建议直接拉镜像而非源码：

```bash
docker pull registry.klein.example/backend:v2026-05-14
docker tag registry.klein.example/backend:v2026-05-14 kleinai/backend:latest
```

---

## 4. 配置

### 4.1 主控后台 → 集群节点 → 添加，拿到三件套

```
KLEIN_NODE_ID=agent-hk-01
KLEIN_NODE_TOKEN=eyJub2RlIjoiYWdlbnQtaGstMDEi...        # 一次性 bootstrap token，~ 60min 失效
KLEIN_CONTROL_URL=https://admin.klein.example
```

### 4.2 写 `.env.agent`

`/opt/klein-agent/env/.env.agent`（`chmod 600`）：

```ini
KLEIN_NODE_ID=agent-hk-01
KLEIN_NODE_TOKEN=eyJub2RlIjoiYWdlbnQtaGstMDEi...
KLEIN_CONTROL_URL=https://admin.klein.example

# 该节点对外的 base URL，必须与后台「public_host」一致
KLEIN_NODE_PUBLIC_URL=https://hk01.cdn.klein.example

# 监听
KLEIN_AGENT_PORT=27180           # agent HTTP（容器内监听，不直接暴露公网）
KLEIN_EDGE_HTTP_PORT=27080       # nginx HTTPS 出口（公网）

# 容量
KLEIN_AGENT_MAX_CONCURRENCY=16
KLEIN_AGENT_STORAGE_ROOT=/var/klein/storage/public
KLEIN_AGENT_STORAGE_QUOTA_GB=80  # 超过自动淘汰最旧
KLEIN_AGENT_PROVIDERS=gpt,grok,adobe   # 与主控注册时填的 provider_scope 对齐

# 心跳 / 拉取
KLEIN_AGENT_HEARTBEAT_SEC=5
KLEIN_AGENT_LEASE_SEC=2

# 时区 / 日志
TZ=Asia/Shanghai
KLEIN_LOG_DIR=/app/logs
```

> 没有数据库、没有 Redis、没有 AES_KEY —— agent 全靠 handshake 拿运行参数。**这是为了在节点被入侵时的最小爆炸半径**。

### 4.3 nginx 配置

`/opt/klein-agent/deploy/nginx-edge.conf`：

```nginx
upstream klein_agent {
  server agent:27180;
  keepalive 32;
}

server {
  listen 27080 ssl http2;
  server_name hk01.cdn.klein.example;

  ssl_certificate     /etc/nginx/certs/edge.fullchain;
  ssl_certificate_key /etc/nginx/certs/edge.key;
  ssl_protocols TLSv1.2 TLSv1.3;
  ssl_ciphers HIGH:!aNULL:!MD5;

  # CORS（让 klein.example 的 fetch 能拿到）
  add_header Access-Control-Allow-Origin  "https://klein.example" always;
  add_header Access-Control-Allow-Methods "GET, HEAD, OPTIONS" always;
  add_header Vary "Origin" always;

  add_header X-Content-Type-Options nosniff always;
  add_header Referrer-Policy strict-origin-when-cross-origin always;

  client_max_body_size 1m;     # 边缘节点不接收上传

  # 健康检查（探活用，不签）
  location = /healthz {
    proxy_pass http://klein_agent/healthz;
  }

  # 下载入口：先打到 agent 校验 ticket → X-Accel-Redirect 到本地磁盘
  location /d/ {
    proxy_pass http://klein_agent;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_read_timeout 30s;
    proxy_buffering off;
  }

  # X-Accel 内部位置：仅 agent 可触发
  location /_local/ {
    internal;
    alias /var/klein/storage/public/;
    add_header Access-Control-Allow-Origin  "https://klein.example" always;
    add_header Cache-Control "public, max-age=86400" always;
    sendfile on;
    tcp_nopush on;
    aio threads;
  }

  # 默认页（防探测）
  location / {
    return 404;
  }
}
```

### 4.4 docker-compose

`/opt/klein-agent/deploy/docker-compose.agent.yml`：

```yaml
services:
  agent:
    image: kleinai/backend:latest
    container_name: klein-agent
    restart: always
    command: ["/app/agent"]
    env_file: /opt/klein-agent/env/.env.agent
    expose: ["27180"]
    volumes:
      - /var/klein/storage:/var/klein/storage
      - /opt/klein-agent/logs:/app/logs
    deploy:
      resources:
        limits: { cpus: '4', memory: 4G }
    networks: [edge]

  nginx:
    image: nginx:1.27-alpine
    container_name: klein-edge-nginx
    restart: always
    depends_on: [agent]
    ports:
      - "27080:27080"
    volumes:
      - /opt/klein-agent/deploy/nginx-edge.conf:/etc/nginx/conf.d/default.conf:ro
      - /opt/klein-agent/certs:/etc/nginx/certs:ro
      - /var/klein/storage:/var/klein/storage:ro
      - /opt/klein-agent/logs/nginx:/var/log/nginx
    networks: [edge]

networks:
  edge:
    driver: bridge
```

> 注意：nginx 必须以 **同样的卷** 只读挂 `/var/klein/storage`，否则 `X-Accel-Redirect` 找不到文件。

---

## 5. 启动 & 注册流程

```bash
cd /opt/klein-agent/deploy
docker compose -f docker-compose.agent.yml up -d
```

启动后 agent 自动做：

```
[01] startup                node_id=agent-hk-01
[02] resolving control...   GET https://admin.klein.example/admin/api/v1/healthz  → 200
[03] handshake              POST /admin/api/v1/cluster/handshake  body={token, public_url, version, hostname}
[04] handshake.ok           hmac_secret_len=32, provider_scope=[gpt grok adobe], max_concurrency=16
[05] heartbeat              POST /admin/api/v1/cluster/heartbeat → 200
[06] lease.tick             0 tasks
```

主控后台「集群节点」上该行应在 ≤ 10s 内变 **在线** + 显示 last_ip / version。

如果 30s 内还没上线：
1. 边缘机器 `curl -sS https://admin.klein.example/admin/api/v1/healthz` 通吗？不通 → 防火墙 / DNS。
2. `docker logs klein-agent | grep handshake` 有 `401 bad token` → token 已过期/已被用，主控后台 **重新生成 token**。
3. 主控查 `SELECT status, last_ip, last_heartbeat_at FROM cluster_node WHERE node_id='agent-hk-01';`。

---

## 6. 验证下载链路

1. 在主控创建一个生图任务，观察 `generation_task` 的 `claim_node_id`：

   ```sql
   SELECT task_id, claim_node_id, status, finished_at
     FROM generation_task
    ORDER BY id DESC LIMIT 5;
   ```

   应该看到该任务被 `agent-hk-01` 接走。

2. 任务跑完后 `download_locator` 应该有一行：

   ```sql
   SELECT asset_key, node_id, rel_path, size_bytes, sha256
     FROM download_locator
    WHERE asset_key LIKE '<task_id>%';
   ```

3. 用户态点开图片，浏览器 DevTools Network 应该看到 **两条请求**：
   - `GET https://klein.example/api/v1/gen/cached/<...>` → 302
   - `GET https://hk01.cdn.klein.example/d/<ticket>` → 200 + image bytes

4. 边缘机器 `tail -f /var/log/nginx/access.log`，应该看到：

   ```
   1.2.3.4 - - [...] "GET /d/eyJ... HTTP/2.0" 200 524288 "https://klein.example/..." "Mozilla/5.0..."
   ```

---

## 7. 边缘节点日常运维

### 7.1 磁盘清理

agent 自带 LRU：当 `/var/klein/storage` 占用 > `KLEIN_AGENT_STORAGE_QUOTA_GB` 的 95% 时，按 atime 倒序删，直到回落到 80%。

手动强清：

```bash
docker exec klein-agent /app/agent gc --force --keep-hours=24
```

### 7.2 日志

- `agent`：`/opt/klein-agent/logs/agent.log`，按天滚动 + zstd 压缩，保留 30 天；
- `nginx`：`/opt/klein-agent/logs/nginx/access.log` + `error.log`。

### 7.3 升级

```bash
# 主控先升好，确认 schema/HMAC payload 没破坏向后兼容
docker pull registry.klein.example/backend:v2026-05-14
docker compose -f /opt/klein-agent/deploy/docker-compose.agent.yml up -d --no-deps agent
docker compose -f /opt/klein-agent/deploy/docker-compose.agent.yml exec nginx nginx -s reload
```

agent 启动后会发起 handshake，主控可以拒绝低版本：`HTTP 426 Upgrade Required`，看到这个就要先升 agent。

### 7.4 节点下线

1. 主控后台 → 集群节点 → 该节点点 **维护中**：调度立即停发新任务；
2. 等 `inflight=0`（一般 < 5 min）；
3. ssh 到机器：`docker compose down`；
4. 主控后台再点 **删除**，会清掉该 `node_id` 对应的所有 `download_locator`（已 302 的旧链接会回退到主控本地或其他节点）。

### 7.5 被入侵处置

主控后台 → 集群节点 → **吊销**：

- `hmac_secret` 立即作废，agent 进程拿到 401 后会停消费；
- 该节点已签出的 `ticket` 也会被验签拒绝（因为 ticket 用 node_secret 签）；
- 已经在浏览器缓存里的图不影响；
- 后续主控不再 302 到该节点。

---

## 8. 一台机器跑多个 agent？

**不推荐**。如果业务上必须（如多套环境隔离），区分变量：

| Stack | 端口 | 卷 |
|-------|------|-----|
| A | 27080 / 27180 | `/var/klein/storage-a` |
| B | 28080 / 28180 | `/var/klein/storage-b` |

并各起一份 compose + 各注册一个 node_id。本地联调本来就这么干，见 `LOCAL_CLUSTER.md`。

---

## 9. FAQ

**Q1：边缘节点掉线了，已经发出去的 302 链接还能用吗？**
A：5 分钟内仍有效（ticket 是节点签的），但你的边缘机器没回应，浏览器超时。用户重试 → 主控会重新查 locator 选其他可用节点。

**Q2：能让一个 agent 只做"代理下载"不跑生成任务吗？**
A：能。把 `provider_scope` 留空（或后台勾 `download_only`），调度器就不会发生成任务给它，但热文件复制阶段（阶段 2）会主动把内容拉到它本地。

**Q3：边缘节点的磁盘文件加密吗？**
A：不加密 —— 生成结果（图、视频）本来就是要给用户看的。如果业务上要"链接过期后磁盘也要彻底删除"，靠 agent 的 LRU + 短 TTL（默认 30 天）即可。

**Q4：能不能把图片同时同步到 OSS？**
A：能。主控保留现有 `uploadCachedAssetToOSS`，对接 `cluster.archive_to_oss=on` 后，主控调度器选 `archive` 节点（不参与下载，专门归档）异步上传，上传成功后 locator 多一行 `node_id=oss`，进一步分流。
