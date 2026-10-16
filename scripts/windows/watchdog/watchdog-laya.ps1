# ===========================================================================
# watchdog-laya.ps1 -- keep the Laya service alive on Windows.
#
# If 127.0.0.1:<Port> is not listening, the scheduled task (default:
# LayaEngineServer, registered by install-autostart.cmd) is started and the
# event is appended to logs\watchdog.log.
#
# Task Scheduler has its own restart-on-failure, but it does not notice a
# process that is alive while the port stays closed (for example after a CUDA
# driver reset), so this external check exists as well.
#
# Run cadence: every 5 minutes via setup-watchdog.ps1 (or launch
# watchdog-hidden.vbs from a Task Scheduler entry to avoid a console flash).
# ===========================================================================
param(
  [string]$Root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path,
  [int]$Port = 8095,
  [string]$TaskName = 'LayaEngineServer'
)

$ErrorActionPreference = 'Continue'
$logDir = Join-Path $Root 'logs'
if (-not (Test-Path -LiteralPath $logDir)) { New-Item -ItemType Directory -Force -Path $logDir | Out-Null }
$logFile = Join-Path $logDir 'watchdog.log'

$ok = $false
try {
  $c = New-Object Net.Sockets.TcpClient
  $c.Connect('127.0.0.1', $Port)
  $ok = $c.Connected
  $c.Close()
} catch { $ok = $false }

if (-not $ok) {
  try {
    Start-ScheduledTask -TaskName $TaskName -ErrorAction Stop
    $line = (Get-Date -Format 'yyyy-MM-dd HH:mm:ss') + " engine down -> task '$TaskName' started"
  } catch {
    $line = (Get-Date -Format 'yyyy-MM-dd HH:mm:ss') + " engine down -> failed to start task '$TaskName': $($_.Exception.Message)"
  }
  Add-Content -Path $logFile -Value $line
}
