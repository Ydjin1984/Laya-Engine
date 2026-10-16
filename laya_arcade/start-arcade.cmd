@echo off
REM ===========================================================================
REM start-arcade.cmd - start the arcade backend (page + server-side training +
REM proxy to the Laya instances) on http://127.0.0.1:8099/.
REM
REM serve.js runs the evolutionary training itself and stores it in
REM state\trainer-state.json, so while this process is alive training continues
REM even with the browser tab closed.
REM
REM ASCII-only and CRLF line endings are required, see ..\start-laya.cmd.
REM ===========================================================================
setlocal EnableExtensions
cd /d "%~dp0"

set "ROOT=%~dp0"
set "PORT=8099"
set "ADDR=127.0.0.1:%PORT%"
set "LOG=%ROOT%logs\arcade.log"
set "WAIT_TRIES=20"

if not exist "%ROOT%logs" mkdir "%ROOT%logs" >nul 2>nul

set "NODE="
for /f "delims=" %%N in ('where node 2^>nul') do if not defined NODE set "NODE=%%N"
if not defined NODE (
  echo [ARCADE] ERROR: node.js not found in PATH - install Node 20+ and retry.
  exit /b 1
)
if not exist "%ROOT%serve.js" (
  echo [ARCADE] ERROR: %ROOT%serve.js not found.
  exit /b 1
)

if not exist "%ROOT%state" mkdir "%ROOT%state" >nul 2>nul

where curl.exe >nul 2>nul
if errorlevel 1 (
  echo [ARCADE] ERROR: curl.exe is required to wait for readiness.
  exit /b 1
)

REM Already up? Keep it: the server holds the trained populations in memory.
curl.exe -f -s -o nul --max-time 3 "http://%ADDR%/api/snapshot"
if not errorlevel 1 (
  echo [ARCADE] already running: http://%ADDR%/
  start "" "http://%ADDR%/"
  exit /b 0
)

netstat -ano -p tcp | findstr /i "LISTENING" | findstr /c:"%ADDR%" >nul 2>nul
if not errorlevel 1 (
  echo [ARCADE] ERROR: port %PORT% is busy but the arcade does not answer.
  echo [ARCADE] Stop the process holding it, or run: stop-all.cmd
  exit /b 1
)

REM node ??????????? ????????, ??? ????????? "cmd /c": ?????? ?????????? ????
REM ??????????? ?????? ? ????????? ??? ????????? ? ??????? ?????? ? ????? -
REM ??????? ??? ????????? ??? "?????? ?? ????????" ??? ????? ?????? ????.
echo [ARCADE] starting backend: %NODE% serve.js  (log: %LOG%)
start "laya-arcade-%PORT%" /D "%ROOT%" /min "%NODE%" "%ROOT%serve.js"

echo [ARCADE] waiting for the backend to answer...
set /a tries=0
:wait
set /a tries+=1
curl.exe -f -s -o nul --max-time 5 "http://%ADDR%/api/snapshot"
if not errorlevel 1 goto ready
if %tries% geq %WAIT_TRIES% goto failed
ping -n 2 127.0.0.1 >nul 2>nul
goto wait

:ready
echo [ARCADE] ready: http://%ADDR%/
start "" "http://%ADDR%/"
echo.
echo [ARCADE] Laya instances are started separately:
echo [ARCADE]   start-all.cmd      - everything (service + fleet + arcade)
echo [ARCADE]   ..\start-laya.cmd 8101   - one extra instance for the arcade
exit /b 0

:failed
echo [ARCADE] ERROR: backend did not answer after %WAIT_TRIES% waits. Last log lines:
powershell -NoProfile -Command "if (Test-Path '%LOG%') { Get-Content -Tail 30 -Path '%LOG%' } else { Write-Host 'log file not found' }"
exit /b 1
