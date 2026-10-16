@echo off
REM ===========================================================================
REM fleet-launcher.cmd <port> - start one Laya instance (thin wrapper).
REM
REM Kept for compatibility with existing shortcuts: all checks, logs and waiting
REM for /ready live in ..\..\start-laya.cmd, so there is exactly one place with
REM the launch logic.
REM
REM ASCII-only and CRLF line endings are required.
REM ===========================================================================
setlocal EnableExtensions
set "PORT=%~1"
if "%PORT%"=="" (
  echo usage: fleet-launcher.cmd PORT     example: fleet-launcher.cmd 8101
  exit /b 1
)
call "%~dp0..\..\start-laya.cmd" %PORT%
exit /b %errorlevel%
