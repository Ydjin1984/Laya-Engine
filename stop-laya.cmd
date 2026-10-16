@echo off
REM ===========================================================================
REM stop-laya.cmd - stop a local Laya decision service instance.
REM
REM Companion of start-laya.cmd. Only touches a process that actually IS
REM laya-server.exe and holds the port - anything else is reported untouched.
REM
REM WARNING: this file must stay ASCII-only AND use CRLF line endings.
REM cmd.exe misparses LF-only batch files and Cyrillic text breaks the parser
REM on some code pages. Also: no unquoted parentheses inside if/for blocks -
REM cmd closes the block early and the parser dies with "unexpected :".
REM
REM Usage:  stop-laya.cmd [port]
REM   port        listen port, default 8095 (fleet uses 8101..8105)
REM ===========================================================================
setlocal EnableExtensions
cd /d "%~dp0"

set "PORT=%~1"
if "%PORT%"=="" set "PORT=8095"

echo %PORT%| findstr /r "^[0-9][0-9]*$" >nul
if errorlevel 1 (
  echo [LAYA] ERROR: bad port "%PORT%" - expected a number, e.g. 8095.
  exit /b 1
)
set "ADDR=127.0.0.1:%PORT%"

REM ------------------------------------------------------------- find holder
set "PID="
for /f "tokens=5" %%a in ('netstat -ano -p tcp ^| findstr /i "LISTENING" ^| findstr /c:"%ADDR%"') do set "PID=%%a"

if not defined PID (
  echo [LAYA] port %PORT%: no listener - already stopped.
  exit /b 0
)

REM ------------------------------------------------- verify it is laya-server
tasklist /fi "PID eq %PID%" /fo csv /nh > "%TEMP%\laya_stop_pid.txt" 2>nul
findstr /i "laya-server.exe" "%TEMP%\laya_stop_pid.txt" >nul
if errorlevel 1 (
  echo [LAYA] ERROR: port %PORT% is held by another process - PID %PID%:
  type "%TEMP%\laya_stop_pid.txt"
  del "%TEMP%\laya_stop_pid.txt" >nul 2>nul
  echo [LAYA] Refusing to stop it. Terminate it manually if that is intended.
  exit /b 1
)
del "%TEMP%\laya_stop_pid.txt" >nul 2>nul

echo [LAYA] stopping laya-server.exe - PID %PID% - on port %PORT% ...
taskkill /pid %PID% >nul 2>nul
if errorlevel 1 (
  echo [LAYA] graceful stop did not take - forcing.
  taskkill /f /pid %PID% >nul 2>nul
)

REM ------------------------------------------------------ wait for the port
set /a tries=0
:wait
netstat -ano -p tcp | findstr /i "LISTENING" | findstr /c:"%ADDR%" >nul 2>nul
if errorlevel 1 (
  echo [LAYA] stopped.
  exit /b 0
)
set /a tries+=1
if %tries% geq 20 (
  echo [LAYA] ERROR: port %PORT% is still busy after the forced kill.
  exit /b 1
)
ping -n 2 127.0.0.1 >nul 2>nul
goto wait
