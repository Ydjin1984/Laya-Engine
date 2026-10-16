# stop-laya-fleet.ps1 — останавливает ТОЛЬКО дополнительные экземпляры Laya
# (порты 8101..8105). Базовый сервис на 8095 не трогается: им пользуются Киборг,
# DeepSeec и аркада.
#
# Запуск: powershell -ExecutionPolicy Bypass -File stop-laya-fleet.ps1

param([int[]]$Ports = @(8101, 8102, 8103, 8104, 8105))

$ErrorActionPreference = 'Continue'
$killed = @()

foreach ($port in $Ports) {
  # Имя переменной НЕ $pid: $PID — автоматическая переменная PowerShell, только
  # для чтения. Из-за этого прежняя версия скрипта падала на первом же порту и
  # флот не останавливался вообще.
  $lines = netstat -ano -p tcp | Select-String -Pattern "127\.0\.0\.1:$port\s+.*LISTENING"
  foreach ($line in $lines) {
    $parts = ($line.ToString() -split '\s+') | Where-Object { $_ -ne '' }
    $procId = $parts[-1]
    if ($procId -notmatch '^\d+$') { continue }
    try {
      $proc = Get-Process -Id ([int]$procId) -ErrorAction Stop
      if ($proc.ProcessName -ne 'laya-server') {
        Write-Host "[LAYA] порт $port держит процесс $($proc.ProcessName) (pid $procId) — не трогаю" -ForegroundColor Yellow
        continue
      }
      Stop-Process -Id ([int]$procId) -Force -ErrorAction Stop
      $killed += "$port (pid $procId)"
    } catch {
      Write-Host "[LAYA] не удалось остановить pid $procId на порту $port" -ForegroundColor Yellow
    }
  }
}

if ($killed.Count -gt 0) {
  Write-Host ("[LAYA] остановлены: {0}" -f ($killed -join ', ')) -ForegroundColor Green
} else {
  Write-Host "[LAYA] дополнительные экземпляры не найдены"
}

Write-Host ""
nvidia-smi --query-gpu=index,name,memory.used,memory.free --format=csv
