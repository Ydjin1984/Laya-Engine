@echo off
REM ===========================================================================
REM start-laya.cmd - start one local Laya decision service instance.
REM
REM Weights stay local: the model is read from models\ and is NEVER downloaded.
REM If a required file is missing the script says so and stops.
REM
REM WARNING: this file must stay ASCII-only AND use CRLF line endings.
REM cmd.exe misparses LF-only batch files (a single line gets split and the
REM fragments run as commands - the classic "is not recognized as an internal
REM or external command" storm) and Cyrillic text breaks the parser on some
REM code pages.
REM
REM Usage:  start-laya.cmd [port] [/cpu] [/no-warmup]
REM   port        listen port, default 8095 (fleet uses 8101..8105)
REM   /cpu        run on the CPU (slow, debugging only)
REM   /no-warmup  skip the warm-up call
REM ===========================================================================
setlocal EnableExtensions
cd /d "%~dp0"

set "ROOT=%~dp0"
set "EXE=%ROOT%bin\laya-server.exe"
set "MODEL=%ROOT%models\multilingual-fp16.onnx"
set "TOKENIZER=%ROOT%models\tokenizer.json"
set "ORT=%ROOT%bin\onnxruntime.dll"
set "CUDA_DIR=%ROOT%third_party\cuda12"
set "LOG=%ROOT%logs\laya-server.log"
set "DEVICE=cuda"
REM On this machine the only GPU is the GTX 1650 and it sits at index 0.
REM (The old "RTX 3060 = #1" note belonged to the previous box.)
set "DEVICE_ID=0"
set "PORT=8095"
set "WARMUP=true"
set "WAIT_TRIES=75"

REM ------------------------------------------------------------------ args
:parse
if "%~1"=="" goto parsed
if /i "%~1"=="/cpu" (
  set "DEVICE=cpu"
  shift
  goto parse
)
if /i "%~1"=="/no-warmup" (
  set "WARMUP=false"
  shift
  goto parse
)
set "PORT=%~1"
shift
goto parse
:parsed

echo %PORT%| findstr /r "^[0-9][0-9]*$" >nul
if errorlevel 1 (
  echo [LAYA] ERROR: bad port "%PORT%" - expected a number, e.g. 8095.
  exit /b 1
)
set "ADDR=127.0.0.1:%PORT%"

if not exist "%ROOT%logs" mkdir "%ROOT%logs" >nul 2>nul

REM ---------------------------------------------------------------- preflight
if not exist "%EXE%" (
  echo [LAYA] ERROR: %EXE% not found - build it first:  build.cmd
  exit /b 1
)
if not exist "%MODEL%" (
  echo [LAYA] ERROR: model not found: %MODEL%
  echo [LAYA] The model is kept in models\ and is never downloaded automatically.
  echo [LAYA] If it is missing, run fetch-model.cmd once, then start again.
  exit /b 1
)
if not exist "%TOKENIZER%" (
  echo [LAYA] ERROR: tokenizer not found: %TOKENIZER%
  echo [LAYA] If it is missing, run fetch-model.cmd once, then start again.
  exit /b 1
)
if not exist "%ORT%" (
  echo [LAYA] ERROR: onnxruntime not found: %ORT%
  echo [LAYA] Expected the 1.25.0 GPU build of onnxruntime - file onnxruntime-win-x64-gpu in bin\.
  exit /b 1
)
if /i "%DEVICE%"=="cuda" (
  if not exist "%CUDA_DIR%\cudart64_12.dll" (
    echo [LAYA] ERROR: CUDA 12 runtime not found in %CUDA_DIR%
    echo [LAYA] The CUDA provider needs cudart64_12.dll, cublas64_12.dll,
    echo [LAYA] cublasLt64_12.dll, cudnn64_9.dll and cufft64_11.dll there.
    exit /b 1
  )
)

where curl.exe >nul 2>nul
if errorlevel 1 (
  echo [LAYA] ERROR: curl.exe is required to wait for readiness and was not found.
  exit /b 1
)

REM ------------------------------------------------- already running? (cheap)
REM The service loads 617 MB of weights once (about 7 s) and keeps them in VRAM:
REM restarting throws the warm-up away and cuts clients off mid-request.
curl.exe -f -s -o nul --max-time 3 "http://%ADDR%/ready"
if not errorlevel 1 (
  echo [LAYA] port %PORT%: already running and ready - nothing to do.
  exit /b 0
)

netstat -ano -p tcp | findstr /i "LISTENING" | findstr /c:"%ADDR%" >nul 2>nul
if not errorlevel 1 (
  echo [LAYA] ERROR: port %ADDR% is busy but /ready does not answer.
  echo [LAYA] An old or hung process holds it - stop it first:  stop-laya.cmd
  exit /b 1
)

REM ------------------------------------------------------------------- start
echo [LAYA] starting: port=%PORT% device=%DEVICE% gpu=#%DEVICE_ID%
echo [LAYA] model: %MODEL%
echo [LAYA] log:   %LOG%
start "laya-server-%PORT%" /min "%EXE%" -device %DEVICE% -device-id %DEVICE_ID% -model "%MODEL%" -tokenizer "%TOKENIZER%" -ort "%ORT%" -addr %ADDR% -log "%LOG%" -warmup=%WARMUP%

echo [LAYA] waiting for the model to load (first start takes about 10 s)...
set /a tries=0
:wait
set /a tries+=1
curl.exe -f -s -o nul --max-time 5 "http://%ADDR%/ready"
if not errorlevel 1 goto ready
if %tries% geq %WAIT_TRIES% goto failed
ping -n 3 127.0.0.1 >nul 2>nul
goto wait

:ready
echo [LAYA] ready: http://%ADDR%/info
exit /b 0

:failed
echo [LAYA] ERROR: port %PORT% not ready after %WAIT_TRIES% waits. Last log lines:
powershell -NoProfile -Command "if (Test-Path '%LOG%') { Get-Content -Tail 25 -Path '%LOG%' } else { Write-Host 'log file not found' }"
exit /b 1
