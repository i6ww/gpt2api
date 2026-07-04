# 集群架构概览 · Control-Plane + Edge-Agent

> 适用版本：gpt2api 2026-05-14 起。
> 阅读顺序：先看本文 → 选「主控部署」（`MAIN_DEPLOY.md`）或「边缘节点部署」（`AGENT_DEPLOY.md`），最后看「本地多端口联调」（`LOCAL_CLUSTER.md`）。

---

## 1. 设计目标

| 目标 | 单机现状 | 集群目标 |
|------|----------|----------|
| 用户/计费/账号池/管理后台数据 | 单库单 Redis | **保留单库单 Redis，集中在主控** |
| 生图/视频任务执行 | 主控 `worker` 进程 | **任意数量边缘节点横向扩容**（按 provider 隔离） |
| 生成文件下载带宽 | 主控 nginx 静态吐文件 | **就近边缘节点直接吐文件**，主控只发签名跳转 |
| 接入新节点 | 改 compose、重启 | 主控后台一次 `添加节点` 即可，节点零业务配置 |
| 自建多机房多子域名下载 | 不支持 | 每节点一个 `public_host`（独立子域名 + 独立证书），用户态自动就近 |
| 不依赖云对象存储 | OSS 可选 | **本机磁盘 + 节点间懒拉复制** 即可，OSS 仍兼容 |

简单一句：**主控只管"账本和路由"，边缘节点既是"算力"也是"CDN"**。

---

## 2. 拓扑图

```
                                 公网
                                   │
        ┌──────────────────────────┴──────────────────────────┐
        │                                                     │
        ▼                                                     ▼
┌──────────────────┐                              ┌──────────────────────┐
│ 主站 (Control)    │ ── HTTPS（admin/inner-api） │ 边缘节点 agent-hk-01  │
│ klein.example     │ <───── 心跳 / lease / 回调 ─│ hk01.cdn.example      │
│                   │                              │   /d/<ticket>         │
│  user-web 17080   │                              │   /healthz            │
│  admin-web 17088  │                              │   nginx X-Accel       │
│  openai    17200  │                              │   ↓ 本地磁盘          │
│  api       17180  │                              │   /var/klein/storage  │
│  admin     17188  │                              └──────────────────────┘
│  worker(control)  │                                          ▲
│  mysql     13306  │                              ┌───────────┘
│  redis     16379  │                              │
└──────────────────┘                              ┌──────────────────────┐
        ▲                                          │ 边缘节点 agent-sg-02  │
        │                                          │ sg02.cdn.example      │
        │  /api/v1/gen/cached/* → 302 ────────────▶│   /d/<ticket>         │
        │                                          └──────────────────────┘
        │
   用户浏览器
```

- 主控暴露 `klein.example`（用户/管理/OpenAI）。
- 每台边缘机器 **独立公网 IP + 独立子域名**（可以挂在不同机房、不同 IP 段、不同 ISP）。
- 用户态浏览器最终是直接从边缘子域名下载，主控只负责"发签名 URL"。
- 边缘节点 **不直连 MySQL/Redis**，所有数据交互走 HTTPS + HMAC 签名到主控 admin 端口（17188）。

---

## 3. 角色与职责

### 3.1 主控（Control-Plane）

跑下列 **6 个进程**（与现有 `deploy/docker-compose.yml` 完全一致）：

| 进程 | 端口 | 说明 |
|------|------|------|
| `mysql` | 13306 | 唯一权威数据库 |
| `redis` | 16379 | 缓存 / 限速 / asynq queue |
| `api` | 17180 | 用户 API |
| `admin` | 17188 | 管理后台 API + **集群内部接口（带 HMAC）** |
| `openai` | 17200 | OpenAI 兼容入口 |
| `worker` | - | asynq 消费 + 本机也可作为一个 agent（默认开启） |
| `nginx` | 17080/17088/17200 | 反代 + TLS 终结 |

**新增能力**：
- `admin` 二进制新增一组 `/admin/api/v1/cluster/*` 内部接口：
  - `POST /cluster/lease`  agent 拉一批待跑任务（claim 原子化）
  - `POST /cluster/heartbeat` agent 每 5s 心跳（带 inflight、cpu、mem）
  - `POST /cluster/progress` 上报进度
  - `POST /cluster/result`  上报最终结果 + 本地 `rel_path`
  - `POST /cluster/handshake` 节点启动握手
  - 全部走 `HMAC-SHA256(timestamp + method + path + sha256(body))` 头：`X-Klein-Node`, `X-Klein-Ts`, `X-Klein-Sig`
- `api` 二进制把 `GET /api/v1/gen/cached/*path` 改成 **查 `download_locator` → 302 跳到 `https://<edge>/d/<ticket>`**，节点不在则回退到本地 `c.File()`（兼容单机）。

### 3.2 边缘节点（Agent / Edge）

每台机器只跑 **2 个容器**：

| 进程 | 端口 | 说明 |
|------|------|------|
| `agent` (Go) | 27180 | 拉任务、跑 provider、本地写文件、暴露 `/d/<ticket>` |
| `nginx` | 27080 | TLS 终结 + `X-Accel-Redirect` 内部跳转给本地磁盘 |

**只持有 3 个 secret**：
- `KLEIN_NODE_ID`（如 `agent-hk-01`）
- `KLEIN_NODE_TOKEN`（boot-strap token，启动时去主控 handshake，换出真正的 HMAC secret）
- `KLEIN_CONTROL_URL`（如 `https://admin.klein.example`）

启动后：
1. `POST {CONTROL_URL}/admin/api/v1/cluster/handshake` 拿 `hmac_secret`、`provider_scope`、`max_concurrency`、`storage_root`、限速参数。
2. 5s 心跳一次；
3. 每秒 `POST /cluster/lease` 拉一批任务（按 provider_scope 过滤）；
4. 跑完后写本地磁盘 `/var/klein/storage/public/...`，再 `POST /cluster/result` 把 `rel_path` 报回；
5. 用户态下载 → 主控 302 → `https://hk01.cdn.example/d/<ticket>` → 边缘 agent 校验签名 → `X-Accel-Redirect: /_local/<rel_path>` → nginx 直接吐文件。

**重启即"洗白"**：本地磁盘是 cache（主控不依赖），磁盘没了顶多重新跑一次任务（download_locator 失效后主控会自动选其他节点或主控自身兜底）。

### 3.3 主控自身作为 agent（默认）

主控的 `worker` 进程默认 `KLEIN_NODE_ID=control-local` `KLEIN_AGENT_MODE=embedded`，等价于一个特殊的本地 agent：
- 不走 HMAC（同进程直接调 service 层）；
- 但 **同样在 `cluster_node` 表里有一行**，用于调度/统计/下载路由；
- 单机部署时，整个集群只有这一行，行为退化为今天的单机模式（零回归）。

---

## 4. 数据流：以一次「文生图」为例

```
用户          api (17180)          admin/inner (17188)     agent-hk-01
 │  POST /gen/image                                             │
 │────────────▶│                                                 │
 │             │ insert generation_task (status=0)              │
 │   202 task  │                                                 │
 │◀────────────│                                                 │
 │                                                               │
 │             │              ◀────── POST /cluster/lease ──────│
 │             │ UPDATE generation_task                          │
 │             │ SET claim_node_id='hk01',                       │
 │             │     status=1,                                   │
 │             │     claim_lease_until=now()+5min                │
 │             │ WHERE status=0 AND provider IN (...) LIMIT n    │
 │             │ ────── 返回任务 + 解密后凭证 (one-shot) ───────▶ │
 │                                                               │
 │                                                  agent 跑 provider
 │                                                  写本地 /var/klein/storage/...
 │                                                               │
 │             │ ◀───── POST /cluster/result + rel_path ────────│
 │             │ INSERT download_locator(asset_key, node_id,    │
 │             │                         rel_path, sha256)      │
 │             │ UPDATE generation_task SET status=2            │
 │             │ INSERT generation_result(url='cl://<key>')     │
 │                                                               │
 │   GET /gen/cached/<key>                                       │
 │────────────▶│                                                 │
 │             │ SELECT FROM download_locator                    │
 │             │ pick agent-hk-01 by weighted RR                 │
 │             │ ticket = HMAC(node_secret, key+exp)             │
 │   302 https://hk01.cdn.example/d/<ticket>                     │
 │◀────────────│                                                 │
 │                                                               │
 │  GET https://hk01.cdn.example/d/<ticket>                      │
 │──────────────────────────────────────────────────────────────▶│
 │                                                    校验 ticket
 │                                                    X-Accel-Redirect → 本地磁盘
 │  200 image/png ...                                            │
 │◀──────────────────────────────────────────────────────────────│
```

### 关键点
1. **凭证一次性下发**：主控 lease 时即解密 account 凭证、塞进任务体（AES 加密短 ttl 通道），agent 跑完即弃。agent 进程崩了/磁盘炸了，凭证不会泄漏。
2. **签名 ticket 5 分钟过期**：路径绑死、节点绑死、过期绑死、不能重放别的资源。
3. **任务 lease 5 分钟兜底**：agent 心跳超时 / 进程崩，下一个 lease 周期会被另一个 agent 抢回去（用 `claim_lease_until < now()` 判定）。

---

## 5. 调度策略

### 5.1 任务调度（control → agent）

主控 `/cluster/lease` 一次返回最多 `min(node.max_concurrency - node.inflight, 5)` 条任务，按以下顺序过滤：

1. `provider_scope` 命中：节点只能跑自己配置的 provider（`gpt` / `grok` / `adobe`）。
2. `status=0` 且 `claim_lease_until IS NULL OR claim_lease_until < now()`。
3. 加 `FOR UPDATE SKIP LOCKED` 防多 agent 抢到同一行（MySQL 8.0）。
4. 按 `priority DESC, created_at ASC` 排序。

### 5.2 节点选择（下载路由）

`/api/v1/gen/cached/*` 命中 locator 时，按以下顺序挑节点：

1. 节点 `status=1`、`last_heartbeat_at > now()-90s`、`download_only OR provider_scope.* != empty`；
2. 该节点持有该 `asset_key`（locator 表）；
3. 加权随机：`weight * (1 - inflight_rate)`，让低负载节点优先；
4. 如果用户 `X-Forwarded-For` 已知归属国（Cloudflare CF-IPCountry / 自检），同国优先；
5. 实在没有可用节点 → 主控本地 `c.File()` 兜底（前提是主控也持有该 locator，或主控 storage 卷上还在）。

### 5.3 热文件复制（可选 · 阶段 2）

主控按 5 分钟一个 batch，把最近 1h 内下载 ≥ 10 次的 `asset_key` 推一个"复制任务"：
- 选另一个低负载节点；
- agent 调 `GET /cluster/asset/{key}/source` 拿到原节点签名 URL，拉过来落本地；
- 主控 insert 一行新的 `download_locator(node_id=新节点, ...)`，路由层下一次就能挑到。

阶段 1 默认 **关闭** 这个能力，只做单节点持有 → 单节点下载。

---

## 6. 安全模型

### 6.1 双向认证

- **主控 → agent**：所有 agent 接口都不开放给公网用户；agent 接受 `Authorization: HMAC <node-id>:<sig>` 头，用 handshake 阶段分配的 secret 校验。
- **agent → 主控**：所有 `/admin/api/v1/cluster/*` 接口走相同 HMAC 验证 + IP 白名单（运维在主控注册节点时填 agent 出口 IP）。
- 时间戳 ± 30 秒，过期拒绝。
- `Sig = base64(hmac_sha256(secret, ts + "\n" + method + "\n" + path + "\n" + sha256_hex(body)))`。

### 6.2 凭证保护

- 账号池密文 `credential_enc` **只在主控** 持有 AES key。
- lease 给 agent 时，主控解密后用 **AES-GCM(node_secret, nonce)** 重新封一遍，agent 解封后只用一次。
- 任务 metadata 不写凭证，只写 `account_id`，便于审计。

### 6.3 下载签名

```
ticket = node_id + "." + base64url({
  k: asset_key,
  e: expires_unix,
  s: shape (full / thumb),
  n: nonce
}) + "." + base64url(hmac_sha256(node_secret, payload))
```

- 5 分钟过期；
- 与 node_id 绑定，跨节点无法重放；
- 重放（同 ticket 多次访问）放行（图片本来就要被 CDN 缓存）；
- 节点本地用 lru-cache(2048) 缓存最近见过的 ticket，防止针对 nonce 的 DDoS。

---

## 7. 容量模型（粗算）

单节点 8c16g 普通云主机：

| 维度 | 限制 |
|------|------|
| 并发生图任务 | `max_concurrency=16`（CPU 占用主要是图片压缩 + AES 解密 + HTTP I/O） |
| 并发视频任务 | `max_concurrency=4`（grok web 视频 single-tcp ~150s/段） |
| 下载吞吐 | nginx + sendfile，单机 ≈ 800 Mbps（取决于网卡） |
| 心跳间隔 | 5s，~12 KB/s 出站 |
| MySQL 连接 | 1（连接池复用，每个 agent 不直接连 DB） |

**单机房 5 节点** 即可支撑 ~80 并发图任务 / ~20 并发视频任务 / ~4 Gbps 下载，扩容只需加机器跑 `agent` 二进制 + 主控后台点"添加节点"。

---

## 8. 失败模式与回滚

| 故障 | 表现 | 自愈 |
|------|------|------|
| agent 进程崩 | inflight 任务 5 分钟后被其他 agent 重新 lease | 自动 |
| agent 网络断 | 心跳超时 → 主控 30s 内剔出可用列表 | 自动 |
| agent 磁盘炸 | `download_locator` 仍指向该节点 → 用户拿到 404 → 主控收到 5xx 后立刻把该 locator 标记 `status=2` 并 fallback 到其他节点或重跑任务 | 自动（需 api 端 fallback 逻辑） |
| 主控宕 | 全部 agent 心跳失败 → 不消费任务 → 用户 502 | 主控恢复后继续 |
| 节点被入侵 | secret 泄漏 → 后台一键 `revoke node` → 该 node_id 所有签名立即失效 | 手动 |

**完全回滚到单机**：把 `cluster_node` 表清空 + 删主控 `KLEIN_CLUSTER_ENABLED` 环境变量即可，所有代码路径自动退回到 `c.File()` 直读。

---

## 9. 阶段路线

| 阶段 | 内容 | 状态 |
|------|------|------|
| 0 | 文档（本套 4 篇）+ 端口规划 | ✅ 本次 |
| 1 | DB 表 + HMAC + lease/result + edge `/d/<ticket>` + 主控 302 | 🚧 本次开始 |
| 2 | 主控后台「集群节点」管理页 + 节点统计仪表盘 | ⏳ 下一轮 |
| 3 | 热文件懒拉复制 + 地域亲和 | ⏳ 之后 |
| 4 | gRPC 双向流 / NATS 替换轮询（按规模决定要不要做） | ⏳ 之后 |

---

## 10. 端口规范速查（全文唯一来源）

> 修改端口请同步改 `deploy/.env*` 与 `deploy/docs/*.md`，禁止散落写死。

### 10.1 主控（control-plane）

| 端口 | 服务 | 暴露 |
|------|------|------|
| 17080 | user-web (HTTPS) | 公网 |
| 17088 | admin-web (HTTPS) | 公网（限 IP） |
| 17200 | openai 兼容 | 公网 |
| 17180 | api | 内网 / 容器网络 |
| 17188 | admin（含 `/cluster/*`） | 内网 / 容器网络 |
| 17280 | WS | 与 17080 同域 |
| 13306 | MySQL | 内网 |
| 16379 | Redis | 内网 |
| 17590 | pprof | 白名单 |

### 10.2 边缘节点（agent，每台机器同一套）

| 端口 | 服务 | 暴露 |
|------|------|------|
| 27080 | edge nginx → 下载 (HTTPS) | 公网，独立子域名 |
| 27180 | agent HTTP（内网） | 仅容器网络 |
| 27590 | pprof | 白名单 |

### 10.3 本地多端口联调（同机器跑 N 套）

| Stack | 角色 | 端口偏移 | 关键端口 |
|-------|------|----------|----------|
| `control-local` | 主控 | +0 | 17080 / 17088 / 17200 / 13306 / 16379 |
| `agent-1-local` | 边缘 1 | +0 | 27080 / 27180 |
| `agent-2-local` | 边缘 2 | +1000 | 28080 / 28180 |
| `agent-3-local` | 边缘 3 | +2000 | 29080 / 29180 |

详见 `LOCAL_CLUSTER.md`。
