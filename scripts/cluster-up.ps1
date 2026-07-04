# 本地多端口集群一键启动：1 主控 + N 边缘
# 用法：pwsh scripts/cluster-up.ps1 -AgentCount 2
# 文档：deploy/docs/LOCAL_CLUSTER.md

param(
  [int]$AgentCount = 2,
  [switch]$Rebuild
)

$ErrorActionPreference = "Stop"
$root = Resolve-Path (Join-Path $PSScriptRoot "..")
Set-Location $root

$buildArg = if ($Rebuild) { "--build" } else { "" }

Write-Host "[1/3] Starting control plane (klein.local / admin.klein.local / api.klein.local) ..."
docker compose --env-file deploy/env/.env.local up -d $buildArg
if ($LASTEXITCODE -ne 0) { throw "control plane failed to start" }

Start-Sleep -Seconds 6

Write-Host ""
Write-Host "[2/3] Health check"
try { Invoke-RestMethod -Uri "http://127.0.0.1:17180/healthz" -TimeoutSec 5 | Out-Null; Write-Host "  user api 17180 ✔" } catch { Write-Warning "  user api 17180 ✗ $($_.Exception.Message)" }
try { Invoke-RestMethod -Uri "http://127.0.0.1:17188/admin/api/v1/healthz" -TimeoutSec 5 | Out-Null; Write-Host "  admin api 17188 ✔" } catch { Write-Warning "  admin api 17188 ✗ $($_.Exception.Message)" }

Write-Host ""
Write-Host "[3/3] Starting $AgentCount agent(s) ..."
for ($i = 1; $i -le $AgentCount; $i++) {
  $envFile = "deploy/env/.env.agent-$i.local"
  if (-not (Test-Path $envFile)) {
    Write-Warning "$envFile not found - register node in admin UI first and copy the env."
    continue
  }
  Write-Host "  agent-$i ($envFile) ..."
  docker compose -f deploy/docker-compose.cluster-local.yml `
    --env-file $envFile -p "klein-agent-$i" up -d $buildArg
}

Write-Host ""
Write-Host "Status:"
docker ps --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}" | Select-String "klein"

Write-Host ""
Write-Host "Done. URLs:"
Write-Host "  user-web   http://klein.local:17080"
Write-Host "  admin-web  http://admin.klein.local:17088   (admin / admin123)"
Write-Host "  openai     http://api.klein.local:17200/v1"
