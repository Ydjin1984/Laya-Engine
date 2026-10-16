@echo off
REM ===========================================================================
REM install.cmd - one-command install of Laya Engine on Windows.
REM
REM Downloads the weights (650 MB, SHA-256 checked), ONNX Runtime 1.25.0 and,
REM unless /cpu is given, the CUDA 12 / cuDNN 9 libraries; then builds the
REM service (or downloads the release binary) and runs a self-test.
REM
REM Usage:  install.cmd [options passed to scripts\windows\install.ps1]
REM   install.cmd                 GPU install (falls back to CPU if no NVIDIA GPU)
REM   install.cmd -Cpu            CPU-only install, no CUDA downloads
REM   install.cmd -Prebuilt       no Go toolchain needed
REM   install.cmd -Service        + autostart at boot (run from an elevated prompt)
REM   install.cmd -Plugin         + ZCode plugin and skill
REM
REM ASCII-only and CRLF line endings are required, see start-laya.cmd.
REM ===========================================================================
setlocal EnableExtensions
cd /d "%~dp0"

set "PS1=%~dp0scripts\windows\install.ps1"
if not exist "%PS1%" (
  echo [LAYA] ERROR: %PS1% not found.
  exit /b 1
)

powershell -NoProfile -ExecutionPolicy Bypass -File "%PS1%" %*
exit /b %errorlevel%
