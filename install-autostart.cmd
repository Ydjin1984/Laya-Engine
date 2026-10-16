@echo off
REM ===========================================================================
REM install-autostart.cmd - register the Laya decision service in the Windows
REM Task Scheduler so it starts at MACHINE BOOT (not at user logon).
REM
REM The task runs as SYSTEM, so it comes up before anyone logs in and is
REM reachable on 127.0.0.1:8095 right after the machine is switched on.
REM The real work is in tools\autostart.ps1.
REM
REM Requires an elevated prompt: registering a SYSTEM task needs admin rights.
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
  echo [LAYA] ERROR: administrator rights are required to register a SYSTEM task.
  echo [LAYA] Open an elevated command prompt and run this script again.
  exit /b 1
)

powershell -NoProfile -ExecutionPolicy Bypass -File "%PS1%" %*
exit /b %errorlevel%
