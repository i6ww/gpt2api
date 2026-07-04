# 部署文档 · 索引

> 把 gpt2api 从「单机版」演进到「主控 + N 个边缘节点」高并发集群的全套指南。

| 文档 | 何时读 |
|------|--------|
| [`CLUSTER_OVERVIEW.md`](./CLUSTER_OVERVIEW.md) | 第一次接触集群版本，先看这篇了解架构、调用时序、安全模型、端口规范 |
| [`MAIN_DEPLOY.md`](./MAIN_DEPLOY.md) | 上线 / 升级主控时 |
| [`AGENT_DEPLOY.md`](./AGENT_DEPLOY.md) | 新增一台边缘机器时 |
| [`LOCAL_CLUSTER.md`](./LOCAL_CLUSTER.md) | 开发机本地用不同端口跑 1 主控 + N 边缘 联调 |

---

## 快速选择路径

| 我想… | 看 |
|-------|----|
| 把现有单机版升级到集群（无停机） | OVERVIEW §8 兼容性 → MAIN §8 滚动更新 |
| 加一台机器分摊下载带宽 | AGENT 全篇 |
| 本地复现一个故障/做改造 | LOCAL §4-5 |
| 已部署 agent，验证用户端是否 302 到边缘 | AGENT §6 |
| 节点被入侵 / 紧急切回单机 | MAIN §9 |

---

## 版本与兼容性

| 版本 | 必要 Schema | 向后兼容 |
|------|-------------|----------|
| 2026-05-14（首发） | `cluster_node`, `download_locator`, `generation_task.claim_*` | 是（无 cluster_node 行 → 退化单机） |

升级前请先 `git pull` 主控并跑 `goose up`，再升 agent。
