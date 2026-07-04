#!/usr/bin/env bash
# =====================================================
# KleinAI · 主控生产单机一键部署脚本
#
# 用法（在目标服务器以 root 执行一次即可）：
#   curl -fsSL https://raw.githubusercontent.com/<org>/gpt2api/main/deploy/scripts/bootstrap-server.sh \
#     -o /tmp/bootstrap.sh && bash /tmp/bootstrap.sh
#
# 或者把仓库 scp 上去后：
#   bash deploy/scripts/bootstrap-server.sh
#
# 脚本目标：
#   1) 装 docker + compose plugin（apt 系）
#   2) 创建 /opt/klein/{src,env,backups,logs}
#   3) 拉代码到 /opt/klein/src（也接受 KLEIN_REPO_DIR=/path 跳过 git）
#   4) 生成 /opt/klein/env/.env.prod（用 openssl rand 灌随机 secret）
#   5) docker compose up -d 起整套
#   6) 打开防火墙 80/443
#   7) 提示 DNS A 记录与 LE 自动续期
#
# 幂等：可重复执行；已存在的 .env.prod 不会被覆盖（防误删 secret）。
# =====================================================
set -Eeuo pipefail

KLEIN_HOME="${KLEIN_HOME:-/opt/klein}"
KLEIN_REPO_DIR="${KLEIN_REPO_DIR:-$KLEIN_HOME/src}"
KLEIN_REPO_URL="${KLEIN_REPO_URL:-https://github.com/432539/gpt2api.git}"
KLEIN_REPO_BRANCH="${KLEIN_REPO_BRANCH:-main}"
KLEIN_DOMAIN_USER="${KLEIN_DOMAIN_USER:-gpt2api.com}"
KLEIN_DOMAIN_ADMIN="${KLEIN_DOMAIN_ADMIN:-admin.gpt2api.com}"
KLEIN_ACME_EMAIL="${KLEIN_ACME_EMAIL:-admin@gpt2api.com}"
COMPOSE_FILE="${KLEIN_REPO_DIR}/deploy/docker-compose.prod.yml"
ENV_FILE="$KLEIN_HOME/env/.env.prod"

log()  { printf '\033[1;34m[%s]\033[0m %s\n' "$(date +%H:%M:%S)" "$*"; }
warn() { printf '\033[1;33m[%s WARN]\033[0m %s\n' "$(date +%H:%M:%S)" "$*"; }
die()  { printf '\033[1;31m[%s ERR ]\033[0m %s\n' "$(date +%H:%M:%S)" "$*" >&2; exit 1; }

[[ $EUID -eq 0 ]] || die "must run as root (use sudo -i)"

# ── 1) 系统包 ──────────────────────────────────────────
log "step 1/7  install system packages"
export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y curl wget git ca-certificates gnupg openssl ufw lsb-release jq

# ── 2) docker + compose ───────────────────────────────
if ! command -v docker >/dev/null 2>&1; then
  log "step 2/7  install docker"
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/$(. /etc/os-release; echo "$ID")/gpg \
    | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
  chmod a+r /etc/apt/keyrings/docker.gpg
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
    https://download.docker.com/linux/$(. /etc/os-release; echo "$ID") \
    $(. /etc/os-release; echo "$VERSION_CODENAME") stable" \
    > /etc/apt/sources.list.d/docker.list
  apt-get update -y
  apt-get install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin
  systemctl enable --now docker
else
  log "step 2/7  docker already installed: $(docker --version)"
fi
docker compose version >/dev/null || die "docker compose plugin missing"

# ── 3) 目录 + 代码 ─────────────────────────────────────
log "step 3/7  ensure $KLEIN_HOME layout"
mkdir -p "$KLEIN_HOME"/{env,backups,logs}
chmod 700 "$KLEIN_HOME/env"

if [[ -d "$KLEIN_REPO_DIR/.git" ]]; then
  log "        repo exists, pulling latest"
  git -C "$KLEIN_REPO_DIR" fetch --prune
  git -C "$KLEIN_REPO_DIR" checkout "$KLEIN_REPO_BRANCH"
  git -C "$KLEIN_REPO_DIR" pull --ff-only
elif [[ -d "$KLEIN_REPO_DIR" && ! -d "$KLEIN_REPO_DIR/.git" ]]; then
  warn "        $KLEIN_REPO_DIR exists but is not a git repo — assuming you scp'd it manually"
else
  log "        clone $KLEIN_REPO_URL → $KLEIN_REPO_DIR"
  git clone --branch "$KLEIN_REPO_BRANCH" --depth 1 "$KLEIN_REPO_URL" "$KLEIN_REPO_DIR"
fi
[[ -f "$COMPOSE_FILE" ]] || die "compose file missing: $COMPOSE_FILE"

# ── 4) 生成 / 复用 .env.prod ───────────────────────────
if [[ -f "$ENV_FILE" ]]; then
  log "step 4/7  $ENV_FILE already exists — keep as-is (will not regenerate secrets)"
else
  log "step 4/7  generating fresh secrets in $ENV_FILE"
  R(){ openssl rand -hex 32; }
  P(){ openssl rand -hex 20; }
  KLEIN_JWT_SECRET=$(R)
  KLEIN_JWT_REFRESH_SECRET=$(R)
  KLEIN_AES_KEY=$(R)
  KLEIN_CLUSTER_BOOTSTRAP_SECRET=$(R)
  KLEIN_DOWNLOAD_TICKET_SECRET=$(R)
  KLEIN_MYSQL_ROOT_PASSWORD=$(P)
  KLEIN_MYSQL_PASSWORD=$(P)
  cat > "$ENV_FILE" <<EOF
# ============================================================
# KleinAI · 主控生产 .env (auto-generated $(date -Iseconds))
# 这些 secret 一旦丢失账号池密文将无法解密，备份到 KMS / Bitwarden / 1Password。
# ============================================================
KLEIN_ENV=prod
KLEIN_PUBLIC_BASE_URL=https://${KLEIN_DOMAIN_USER}

# 数据库
KLEIN_MYSQL_PORT=13306
KLEIN_MYSQL_DB=klein_ai
KLEIN_MYSQL_USER=klein
KLEIN_MYSQL_ROOT_PASSWORD=${KLEIN_MYSQL_ROOT_PASSWORD}
KLEIN_MYSQL_PASSWORD=${KLEIN_MYSQL_PASSWORD}
KLEIN_MYSQL_BUFFER=2G
KLEIN_DB_DSN=klein:${KLEIN_MYSQL_PASSWORD}@tcp(mysql:3306)/klein_ai?charset=utf8mb4&parseTime=True&loc=Local

# Redis
KLEIN_REDIS_PORT=16379
KLEIN_REDIS_ADDR=redis:6379
KLEIN_REDIS_PASSWORD=

# 安全（绝不可丢；丢了账号池密文全部解不开）
KLEIN_JWT_SECRET=${KLEIN_JWT_SECRET}
KLEIN_JWT_REFRESH_SECRET=${KLEIN_JWT_REFRESH_SECRET}
KLEIN_JWT_ACCESS_TTL=2h
KLEIN_JWT_REFRESH_TTL=336h
KLEIN_AES_KEY=${KLEIN_AES_KEY}
KLEIN_CLUSTER_BOOTSTRAP_SECRET=${KLEIN_CLUSTER_BOOTSTRAP_SECRET}
KLEIN_DOWNLOAD_TICKET_SECRET=${KLEIN_DOWNLOAD_TICKET_SECRET}

# Provider 上游
KLEIN_PROVIDER_GPT=real
KLEIN_PROVIDER_GROK=real
KLEIN_PROVIDER_ADOBE=real
KLEIN_PROVIDER_PIC2API=real
KLEIN_OPENAI_BASE=https://api.openai.com
KLEIN_GROK_BASE=https://api.x.ai
KLEIN_GPT_BASE_URL=https://api.openai.com
KLEIN_GROK_BASE_URL=https://grok.com

# 跨域：用户站 / 后台两个域名
KLEIN_CORS_ORIGINS=https://${KLEIN_DOMAIN_USER},https://www.${KLEIN_DOMAIN_USER},https://${KLEIN_DOMAIN_ADMIN}

# 集群（单机起步，加节点时从后台注册即可，无需改这里）
KLEIN_CLUSTER_ENABLED=1
KLEIN_EMBEDDED_AGENT=1

# 日志
KLEIN_LOG_LEVEL=info

# Caddy ACME 联系邮箱（续期失败时收提醒）
KLEIN_ACME_EMAIL=${KLEIN_ACME_EMAIL}
TZ=Asia/Shanghai
EOF
  chmod 600 "$ENV_FILE"
  log "        secrets written → $ENV_FILE (chmod 600)"
fi

# ── 5) docker compose up ──────────────────────────────
log "step 5/7  build + up containers (this can take 5-10 min the first time)"
cd "$(dirname "$COMPOSE_FILE")"
docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" build --pull
docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" up -d
docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" ps

# ── 6) 防火墙 ──────────────────────────────────────────
if command -v ufw >/dev/null 2>&1; then
  log "step 6/7  configure UFW (22/tcp, 80/tcp, 443/tcp+udp)"
  ufw allow 22/tcp || true
  ufw allow 80/tcp || true
  ufw allow 443/tcp || true
  ufw allow 443/udp || true   # caddy HTTP/3
  ufw --force enable || true
  ufw status numbered || true
else
  warn "step 6/7  ufw not available — open 80/443 manually on cloud security group"
fi

# ── 7) 完成提示 ────────────────────────────────────────
PUBIP=$(curl -fsS4 https://api.ipify.org || echo "<unknown>")
cat <<TIPS

================================================================
  KleinAI 主控部署完成 ✅
----------------------------------------------------------------
  机器外网 IP: ${PUBIP}
  代码目录   : ${KLEIN_REPO_DIR}
  env 文件   : ${ENV_FILE}        (chmod 600)
  Compose    : ${COMPOSE_FILE}

  请确认以下 DNS A 记录都解析到 ${PUBIP}：
    ${KLEIN_DOMAIN_USER}        → ${PUBIP}
    www.${KLEIN_DOMAIN_USER}    → ${PUBIP}
    ${KLEIN_DOMAIN_ADMIN}       → ${PUBIP}

  Caddy 会在收到第一次 80/443 请求时自动到 Let's Encrypt
  申请证书并保存到 docker volume klein-caddy-data；之后由
  caddy 内置定时器自动续期（提前 30 天），不需要 cron。
  实时进度看：
    docker compose -f ${COMPOSE_FILE} logs -f caddy

  访问入口：
    https://${KLEIN_DOMAIN_USER}/                  用户站
    https://${KLEIN_DOMAIN_ADMIN}/                 管理后台（默认 admin/admin123，登录后立刻改密）
    https://${KLEIN_DOMAIN_USER}/v1/models         OpenAI 兼容 API（需 user key）

  常用命令：
    cd ${KLEIN_REPO_DIR}/deploy
    docker compose -f docker-compose.prod.yml --env-file ${ENV_FILE} ps
    docker compose -f docker-compose.prod.yml --env-file ${ENV_FILE} logs -f api admin
    docker compose -f docker-compose.prod.yml --env-file ${ENV_FILE} restart caddy

  滚动升级：
    bash ${KLEIN_REPO_DIR}/deploy/scripts/update-server.sh
================================================================

TIPS
