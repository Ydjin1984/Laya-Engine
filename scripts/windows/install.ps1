# ===========================================================================
# install.ps1 -- one-shot installer for Laya Engine on Windows.
#
# Steps, in order:
#   1. prerequisites (curl.exe; Go + gcc only if building from source)
#   2. weights + tokenizer          -> tools\fetch-model.ps1   (SHA-256 checked)
#   3. ONNX Runtime 1.25.0          -> scripts\windows\fetch-ort.ps1
#   4. CUDA 12 / cuDNN 9 (GPU only) -> scripts\windows\fetch-cuda.ps1
#   5. engine binary (build or download the release asset)
#   6. optional: boot autostart task (SYSTEM, needs an elevated prompt)
#   7. optional: ZCode plugin + skill
#   8. self-test and next steps
#
# Usage (from the repository root):
#   powershell -ExecutionPolicy Bypass -File scripts\windows\install.ps1
#   ... -Cpu                        # CPU-only install (no CUDA downloads)
#   ... -Prebuilt                   # no Go toolchain needed
#   ... -Service                    # + start at boot (run from an elevated prompt)
#   ... -Plugin                     # + ZCode plugin and skill
#   ... -Port 8095
# ===========================================================================
param(
  [switch]$Cpu,
  [switch]$Prebuilt,
  [switch]$Service,
  [switch]$Plugin,
  [int]$Port = 8095,
  [switch]$Force
)

$ErrorActionPreference = 'Stop'
$Root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
Set-Location $Root

function Info($m) { Write-Host ""; Write-Host "=== [INSTALL] $m ===" -ForegroundColor Cyan }
function Warn($m) { Write-Host "[INSTALL] $m" -ForegroundColor Yellow }
function Fail($m) { Write-Host "[INSTALL] ERROR: $m" -ForegroundColor Red; exit 1 }

if ($env:PROCESSOR_ARCHITECTURE -ne 'AMD64') { Fail "this installer expects 64-bit Windows (x64)." }

# --- 1. prerequisites ---------------------------------------------------------
Info "prerequisites"
if (-not (Get-Command curl.exe -ErrorAction SilentlyContinue)) {
  Fail "curl.exe not found (ships with Windows 10 1803+). Update Windows or download the files manually - see docs\INSTALL.md."
}
$haveGo = [bool](Get-Command go -ErrorAction SilentlyContinue)
$haveGcc = [bool](Get-Command gcc -ErrorAction SilentlyContinue)
if (-not $Prebuilt -and (-not $haveGo -or -not $haveGcc)) {
  Warn "Go or gcc not found - switching to the prebuilt binary (-Prebuilt)."
  if (-not $haveGo)  { Warn "  Go:  https://go.dev/dl/" }
  if (-not $haveGcc) { Warn "  gcc: MSYS2, then: pacman -S mingw-w64-ucrt-x86_64-gcc" }
  $Prebuilt = $true
}

# --- 2. weights ---------------------------------------------------------------
if ((Test-Path 'models\multilingual-fp16.onnx') -and (Test-Path 'models\tokenizer.json')) {
  Info "weights already present - skipping download"
} else {
  Info "downloading weights and tokenizer (650 MB, SHA-256 verified)"
  powershell -NoProfile -ExecutionPolicy Bypass -File tools\fetch-model.ps1
  if ($LASTEXITCODE -ne 0) { Fail "model download failed" }
}

# --- 3. ONNX Runtime ----------------------------------------------------------
if (Test-Path 'bin\onnxruntime.dll') {
  Info "ONNX Runtime already present - skipping download"
} else {
  Info "downloading ONNX Runtime 1.25.0"
  $ortArgs = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', 'scripts\windows\fetch-ort.ps1')
  if ($Cpu) { $ortArgs += '-Cpu' }
  powershell @ortArgs
  if ($LASTEXITCODE -ne 0) { Fail "ONNX Runtime download failed" }
}

# --- 4. CUDA ------------------------------------------------------------------
if (-not $Cpu) {
  if (Test-Path 'third_party\cuda12\cudnn64_9.dll') {
    Info "CUDA 12 / cuDNN 9 already present - skipping download"
  } elseif (Get-Command nvidia-smi -ErrorAction SilentlyContinue) {
    Info "downloading CUDA 12 / cuDNN 9 libraries (~1.5 GB from PyPI wheels)"
    powershell -NoProfile -ExecutionPolicy Bypass -File scripts\windows\fetch-cuda.ps1
    if ($LASTEXITCODE -ne 0) { Warn "CUDA download incomplete - GPU runs will fail; CPU runs (-Cpu) still work." }
  } else {
    Warn "no NVIDIA GPU detected (nvidia-smi missing) - CPU mode."
    $Cpu = $true
  }
}
if ($Cpu) { Warn "CPU mode: the service will run with /cpu (slow, but needs no NVIDIA stack)." }

# --- 5. binary ----------------------------------------------------------------
if ($Prebuilt) {
  Info "downloading the prebuilt binary"
  New-Item -ItemType Directory -Force -Path 'bin' | Out-Null
  $url = 'https://github.com/Ydjin1984/Laya-Engine/releases/latest/download/laya-server-windows-amd64.exe'
  & curl.exe -fL --retry 3 -o 'bin\laya-server.exe' $url
  if ($LASTEXITCODE -ne 0) {
    Fail "download failed: $url`n         Build from source instead: .\build.cmd (needs Go + gcc)."
  }
} elseif (Test-Path 'bin\laya-server.exe') {
  Info "bin\laya-server.exe already present - rebuilding to keep it current"
  cmd /c build.cmd
  if ($LASTEXITCODE -ne 0) { Fail "build failed" }
} else {
  Info "building from source"
  cmd /c build.cmd
  if ($LASTEXITCODE -ne 0) { Fail "build failed" }
}

# --- 6. service ---------------------------------------------------------------
if ($Service) {
  Info "installing the boot autostart task"
  $isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
  if ($Cpu) {
    Warn "autostart task is GPU-only (it registers a SYSTEM task with CUDA). For CPU machines,"
    Warn "create a Task Scheduler entry at logon that runs:  start-laya.cmd /cpu"
  } elseif (-not $isAdmin) {
    Warn "administrator rights are required for the SYSTEM boot task. After this install, open an"
    Warn "elevated prompt and run:  install-autostart.cmd"
  } else {
    cmd /c install-autostart.cmd
    if ($LASTEXITCODE -ne 0) { Warn "autostart registration failed - see the messages above." }
  }
}

# --- 7. ZCode plugin + skill --------------------------------------------------
if ($Plugin) {
  Info "installing the ZCode plugin and skill"
  powershell -NoProfile -ExecutionPolicy Bypass -File scripts\windows\install-plugin.ps1
  if ($LASTEXITCODE -ne 0) { Warn "plugin step failed - see the messages above; the engine itself is installed." }
}

# --- 8. verify ----------------------------------------------------------------
Info "self-test"
$device = if ($Cpu) { 'cpu' } else { 'cuda' }
& bin\laya-server.exe -device $device -device-id 0 -model models\multilingual-fp16.onnx `
  -tokenizer models\tokenizer.json -ort bin\onnxruntime.dll -selftest
$selftestOk = ($LASTEXITCODE -eq 0)

Write-Host ""
Write-Host "================= Laya Engine installed =================" -ForegroundColor Green
Write-Host ("  self-test: " + ($(if ($selftestOk) { 'PASSED' } else { 'FAILED - see the log above' })))
if ($Service -and -not $Cpu) {
  Write-Host "  service:   registered to start at boot (task LayaEngineServer)"
} else {
  Write-Host ("  start:     start-laya.cmd" + $(if ($Cpu) { " /cpu" } else { "" }))
  Write-Host "  stop:      stop-laya.cmd"
}
Write-Host "  status:    http://127.0.0.1:$Port/info"
Write-Host ("  plugin:    " + $(if ($Plugin) { 'installed - enable it in ZCode: Settings -> Plugin Management' } else { 'run scripts\windows\install-plugin.ps1, then enable it in ZCode' }))
Write-Host "  docs:      docs\INSTALL.md, docs\API.md"
Write-Host "========================================================="
if (-not $selftestOk) { exit 1 }
