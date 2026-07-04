# 本地集群一键停止
# 用法：pwsh scripts/cluster-down.ps1 [-AgentCount 3]
param(
  [int]$AgentCount = 3,
  [switch]$Wipe
)

$ErrorActionPreference = "Continue"
$root = Resolve-Path (Join-Path $PSScriptRoot "..")
Set-Location $root

$flag = if ($Wipe) { "-v" } else { "" }

for ($i = 1; $i -le $AgentCount; $i++) {
  $envFile = "deploy/env/.env.agent-$i.local"
  if (Test-Path $envFile) {
    Write-Host "stopping klein-agent-$i ..."
    docker compose -f deploy/docker-compose.cluster-local.yml `
      --env-file $envFile -p "klein-agent-$i" down $flag
  }
}

Write-Host "stopping control plane ..."
docker compose --env-file deploy/env/.env.local down $flag
