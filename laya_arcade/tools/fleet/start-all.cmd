@echo off
REM ===========================================================================
REM start-all.cmd (fleet) - bring up the whole Laya fleet on GPU #1 (RTX 3060).
REM
REM Ports: 8095 (base service other systems use) + 8101..8105 (arcade experts).
REM Instances start strictly one after another: starting them at once used to
REM make the processes die on VRAM allocation.
REM
REM All checks, logging and waiting for /ready live in the project launcher
REM start-laya.cmd, so a busy port is skipped instead of printing a bind error.
REM
REM ASCII-only and CRLF line endings are required.
REM ===========================================================================
setlocal EnableExtensions
cd /d "%~dp0"

set "LAUNCHER=%~dp0..\..\..\start-laya.cmd"
if not exist "%LAUNCHER%" (
  echo [FLEET] ERROR: %LAUNCHER% not found.
  exit /b 1
)

echo [FLEET] starting Laya instances on GPU #1 (RTX 3060)...
for %%P in (8095 8101 8102 8103 8104 8105) do (
  echo.
  echo [FLEET] port %%P
  call "%LAUNCHER%" %%P
  if errorlevel 1 echo [FLEET] port %%P did NOT come up - see the messages above.
)

echo.
echo [FLEET] readiness:
for %%P in (8095 8101 8102 8103 8104 8105) do (
  curl.exe -f -s -o nul --max-time 5 "http://127.0.0.1:%%P/ready" && (echo   port %%P: ready) || (echo   port %%P: NOT ready)
)

echo.
echo [FLEET] GPU state:
nvidia-smi --query-gpu=index,name,memory.used,memory.free --format=csv
echo.
echo [FLEET] arcade page:  http://127.0.0.1:8099/   (start it: ..\..\start-arcade.cmd)
exit /b 0
