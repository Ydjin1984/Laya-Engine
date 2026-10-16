@echo off
REM ===========================================================================
REM build.cmd - gofmt + vet + test + build bin\laya-server.exe
REM Needs gcc (cgo) and a local onnxruntime.dll in bin\.
REM
REM ASCII-only and CRLF line endings are required, see start-laya.cmd.
REM ===========================================================================
setlocal EnableExtensions
set "ROOT=%~dp0"
cd /d "%ROOT%"

REM gcc from MSYS2: cgo does not build on Windows without it.
if exist "E:\msys64\ucrt64\bin\gcc.exe" set "PATH=E:\msys64\ucrt64\bin;%PATH%"
if exist "C:\msys64\ucrt64\bin\gcc.exe" set "PATH=C:\msys64\ucrt64\bin;%PATH%"

set "CGO_ENABLED=1"
where gcc >nul 2>nul
if errorlevel 1 (
  echo [ERROR] gcc not found in PATH - cgo cannot work without it.
  echo Install it with MSYS2: pacman -S mingw-w64-ucrt-x86_64-gcc
  exit /b 1
)

where go >nul 2>nul
if errorlevel 1 (
  echo [ERROR] go not found in PATH.
  exit /b 1
)

if not exist "%ROOT%bin" mkdir "%ROOT%bin" >nul 2>nul

REM A running laya-server.exe holds the file open: Windows refuses to overwrite a
REM running image, so the build is retried into laya-server.new.exe and the user
REM gets the exact command to swap it in. (tasklist cannot be used here: it is
REM denied by policy on some machines and would silently pick the wrong path.)
set "OUT=%ROOT%bin\laya-server.exe"

echo [1/4] gofmt
gofmt -l . > "%TEMP%\laya-gofmt.txt" 2>nul
if errorlevel 1 (
  echo [ERROR] gofmt failed - is Go installed correctly?
  exit /b 1
)
set "UNFORMATTED="
for /f "usebackq delims=" %%i in ("%TEMP%\laya-gofmt.txt") do (
  if not "%%i"=="" set "UNFORMATTED=1"
)
if defined UNFORMATTED (
  echo       formatting files listed by gofmt...
  gofmt -w .
  if errorlevel 1 (
    echo [ERROR] gofmt -w failed.
    exit /b 1
  )
)

echo [2/4] go vet
go vet ./... || exit /b 1

echo [3/4] go test
go test ./... -timeout 10m || exit /b 1

echo [4/4] build
go build -o "%OUT%" .\cmd\laya-server
if errorlevel 1 (
  echo [BUILD] could not write %OUT% - a running service is likely holding it.
  echo [BUILD] building to bin\laya-server.new.exe instead...
  go build -o "%ROOT%bin\laya-server.new.exe" .\cmd\laya-server || exit /b 1
  echo.
  echo [BUILD] done: %ROOT%bin\laya-server.new.exe
  echo [BUILD] To apply it:
  echo [BUILD]   stop-laya.cmd
  echo [BUILD]   move /y "%ROOT%bin\laya-server.new.exe" "%ROOT%bin\laya-server.exe"
  echo [BUILD]   start-laya.cmd
  endlocal
  exit /b 0
)

echo.
echo [BUILD] done: %OUT%
echo [BUILD] start everything (GTX 1650 = GPU #0):  start-all.cmd
echo [BUILD] start the service only:                start-laya.cmd
endlocal
exit /b 0
