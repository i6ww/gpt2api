#!/usr/bin/env bash
# =====================================================
# KleinAI · 主控滚动升级脚本（不影响数据库 / 卷 / Caddy 证书）
#
# 用法：
#   bash /opt/klein/src/deploy/scripts/update-server.sh
#
# 步骤：
#   1) git pull --ff-only
#   2) 重建后端镜像 + 前端镜像
#   3) up -d --no-deps 只重启业务容器（不动 mysql / redis / caddy）
#   4) 健康检查
# =====================================================
set -Eeuo pipefail

KLEIN_HOME="${KLEIN_HOME:-/opt/klein}"
KLEIN_REPO_DIR="${KLEIN_REPO_DIR:-$KLEIN_HOME/src}"
ENV_FILE="${ENV_FILE:-$KLEIN_HOME/env/.env.prod}"
COMPOSE_FILE="$KLEIN_REPO_DIR/deploy/docker-compose.prod.yml"

log() { printf '\033[1;34m[%s]\033[0m %s\n' "$(date +%H:%M:%S)" "$*"; }
die() { printf '\033[1;31m[ERR]\033[0m %s\n' "$*" >&2; exit 1; }

[[ -f "$ENV_FILE" ]] || die "env not found: $ENV_FILE — run bootstrap-server.sh first"
[[ -f "$COMPOSE_FILE" ]] || die "compose not found: $COMPOSE_FILE"

log "git pull"
git -C "$KLEIN_REPO_DIR" fetch --prune
git -C "$KLEIN_REPO_DIR" pull --ff-only

cd "$(dirname "$COMPOSE_FILE")"

log "build images"
docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" build api admin openai worker user-web admin-web

log "rolling restart business containers (db / redis / caddy unchanged)"
docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" up -d --no-deps \
  api admin openai worker user-web admin-web

log "wait 5s then health check"
sleep 5
for svc in api admin openai; do
  cid=$(docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" ps -q "$svc")
  if [[ -z "$cid" ]]; then
    log "  $svc: container missing"
  else
    state=$(docker inspect --format='{{.State.Status}}' "$cid")
    log "  $svc: $state"
  fi
done

log "done. tail logs with:"
echo "  docker compose -f $COMPOSE_FILE --env-file $ENV_FILE logs -f api admin"
