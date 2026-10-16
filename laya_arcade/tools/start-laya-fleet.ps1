# start-laya-fleet.ps1 — поднимает дополнительные экземпляры Laya (8101..8105).
#
# Логика запуска живёт ровно в одном месте — start-laya.cmd в корне проекта:
# проверка файлов, лог, ожидание /ready и пропуск уже занятых портов. Этот скрипт
# только перебирает порты, поэтому не может разойтись с .cmd-версией.
#
# Запуск:  powershell -ExecutionPolicy Bypass -File start-laya-fleet.ps1
# Остановка: powershell -ExecutionPolicy Bypass -File stop-laya-fleet.ps1

param(
  [int[]]$Ports = @(8101, 8102, 8103, 8104, 8105),
  [string]$Root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
)

$ErrorActionPreference = 'Continue'
$launcher = Join-Path $Root 'start-laya.cmd'
if (-not (Test-Path -LiteralPath $launcher)) {
  Write-Host "[LAYA] не найден $launcher" -ForegroundColor Red
  exit 1
}

Write-Host ("[LAYA] поднимаю {0} экземпляров на карте #1 (RTX 3060)" -f $Ports.Count) -ForegroundColor Cyan

$failed = @()
foreach ($port in $Ports) {
  Write-Host ""
  & $launcher "$port"
  if ($LASTEXITCODE -ne 0) { $failed += $port }
}

Write-Host ""
if ($failed.Count -gt 0) {
  Write-Host ("[LAYA] не поднялись порты: {0}" -f ($failed -join ', ')) -ForegroundColor Yellow
  Write-Host "[LAYA] причины - в логе logs\laya-server.log и в выводе выше."
} else {
  Write-Host "[LAYA] флот готов: все запрошенные порты отвечают на /ready" -ForegroundColor Green
}

Write-Host ""
Write-Host "Проверка VRAM:" -ForegroundColor Cyan
nvidia-smi --query-gpu=index,name,memory.used,memory.free --format=csv
Write-Host "Страница аркады: http://127.0.0.1:8099/"
