@echo off
REM ===========================================================================
REM uninstall-autostart.cmd - remove the Laya boot task registered by
REM install-autostart.cmd. Does not touch the service itself: a manually
REM started instance keeps running until stop-laya.cmd.
REM
REM Requires an elevated prompt: removing a SYSTEM task needs admin rights.
REM ASCII-only and CRLF line endings are required, see start-laya.cmd.
REM ===========================================================================
setlocal EnableExtensions
cd /d "%~dp0"

set "PS1=%~dp0tools\autostart.ps1"
if not exist "%PS1%" (
  echo [LAYA] ERROR: %PS1% not found.
  exit /b 1
)

net session >nul 2>nul
if errorlevel 1 (
  echo [LAYA] ERROR: administrator rights are required to remove a SYSTEM task.
  echo [LAYA] Open an elevated command prompt and run this script again.
  exit /b 1
)

powershell -NoProfile -ExecutionPolicy Bypass -File "%PS1%" -Remove %*
exit /b %errorlevel%
