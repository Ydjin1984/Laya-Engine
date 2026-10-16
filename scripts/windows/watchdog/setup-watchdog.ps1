# ===========================================================================
# setup-watchdog.ps1 -- register the Laya watchdog as a scheduled task
# (run once, from an elevated prompt).
#
# The watchdog checks the port every 5 minutes and restarts the service task if
# nothing is listening. It is launched through wscript with a hidden window
# (watchdog-hidden.vbs): "powershell -WindowStyle Hidden" still flashes a
# console with Windows Terminal as the default console host.
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File scripts\windows\watchdog\setup-watchdog.ps1
#   ... -Remove        # unregister
#   ... -IntervalMinutes 5 -Port 8095 -TaskName LayaEngineServer
# ===========================================================================
param(
  [string]$Root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path,
  [int]$Port = 8095,
  [string]$TaskName = 'LayaEngineServer',
  [string]$WatchdogTaskName = 'LayaEngineWatchdog',
  [int]$IntervalMinutes = 5,
  [switch]$Remove
)

$ErrorActionPreference = 'Stop'

if ($Remove) {
  $t = Get-ScheduledTask -TaskName $WatchdogTaskName -ErrorAction SilentlyContinue
  if (-not $t) { Write-Host "[LAYA] watchdog task '$WatchdogTaskName' is not registered."; exit 0 }
  Unregister-ScheduledTask -TaskName $WatchdogTaskName -Confirm:$false
  Write-Host "[LAYA] watchdog task '$WatchdogTaskName' removed."
  exit 0
}

$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) {
  Write-Host "[LAYA] ERROR: administrator rights are required (a SYSTEM/boot task). Run from an elevated prompt." -ForegroundColor Red
  exit 1
}

$vbs = Join-Path $Root 'scripts\windows\watchdog\watchdog-hidden.vbs'
if (-not (Test-Path -LiteralPath $vbs)) { Write-Host "[LAYA] ERROR: $vbs not found." -ForegroundColor Red; exit 1 }

# The .vbs launcher resolves watchdog-laya.ps1 relative to its own folder, so no
# paths have to be passed to the scheduled task.
$action = New-ScheduledTaskAction -Execute 'wscript.exe' -Argument "`"$vbs`""
$trigger = New-ScheduledTaskTrigger -Once -At (Get-Date).AddMinutes(2) `
  -RepetitionInterval (New-TimeSpan -Minutes $IntervalMinutes) `
  -RepetitionDuration (New-TimeSpan -Days 3650)
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
  -MultipleInstances IgnoreNew -ExecutionTimeLimit (New-TimeSpan -Minutes 2)

Register-ScheduledTask -TaskName $WatchdogTaskName -Action $action -Trigger $trigger `
  -Settings $settings -Force | Out-Null

Write-Host "[LAYA] watchdog registered: task '$WatchdogTaskName', every $IntervalMinutes min."
Write-Host "[LAYA] it starts task '$TaskName' when 127.0.0.1:$Port is not listening."
Write-Host "[LAYA] log: $Root\logs\watchdog.log"
Write-Host "[LAYA] remove with: ... -Remove"
