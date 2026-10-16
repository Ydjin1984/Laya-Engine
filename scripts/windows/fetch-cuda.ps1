# ===========================================================================
# fetch-cuda.ps1 -- CUDA 12 / cuDNN 9 libraries for the ONNX Runtime GPU build
# on Windows. Downloads the public NVIDIA wheels from PyPI and copies the DLLs
# into third_party\cuda12\.
#
# Nothing is redistributed by this repository: the wheels are downloaded from
# PyPI, and their use is governed by the NVIDIA CUDA Toolkit EULA
# (https://docs.nvidia.com/cuda/eula/index.html). By downloading you accept it.
#
# Download size: ~1.5 GB (same artifacts `pip install nvidia-cublas-cu12 ...`
# would install). The system CUDA Toolkit (13.x) is NOT a substitute: ONNX
# Runtime 1.25 needs the CUDA 12 names (cudart64_12.dll, cudnn64_9.dll, ...).
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File scripts\windows\fetch-cuda.ps1
#   ... -Force        # re-download even if the DLLs are already present
# ===========================================================================
param(
  [switch]$Force
)

$ErrorActionPreference = 'Stop'
$Root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$Dest = Join-Path $Root 'third_party\cuda12'
$Tmp = Join-Path $Root 'third_party\.cuda-wheel-tmp'

# Wheels whose DLLs the CUDA execution provider needs at load time.
$Packages = @(
  'nvidia-cuda-runtime-cu12',  # cudart64_12.dll
  'nvidia-cublas-cu12',        # cublas64_12.dll, cublasLt64_12.dll
  'nvidia-cudnn-cu12',         # cudnn64_9.dll (+ sub-DLLs)
  'nvidia-cufft-cu12',         # cufft64_11.dll
  'nvidia-cuda-nvrtc-cu12'     # nvrtc64_120*.dll (optional, used by some fusions)
)

function Write-Info($m) { Write-Host "[CUDA] $m" }

$need = Join-Path $Dest 'cudnn64_9.dll'
if ((Test-Path -LiteralPath $need) -and -not $Force) {
  Write-Info "libraries already present in $Dest -- nothing to do (-Force to re-download)."
  exit 0
}

New-Item -ItemType Directory -Force -Path $Dest, $Tmp | Out-Null

$curl = Get-Command curl.exe -ErrorAction SilentlyContinue
if (-not $curl) {
  Write-Host "[CUDA] ERROR: curl.exe not found (ships with Windows 10 1803+)." -ForegroundColor Red
  exit 1
}

foreach ($pkg in $Packages) {
  Write-Info "resolving $pkg ..."
  try {
    $meta = Invoke-RestMethod -Uri "https://pypi.org/pypi/$pkg/json" -UseBasicParsing
  } catch {
    Write-Info "WARNING: cannot reach PyPI for $pkg ($($_.Exception.Message))"
    continue
  }
  $ver = $meta.info.version
  $file = $meta.releases.$ver | Where-Object { $_.filename -like '*win_amd64.whl' } | Select-Object -First 1
  if (-not $file) {
    Write-Info "WARNING: no win_amd64 wheel for $pkg $ver -- skipping."
    continue
  }
  $whl = Join-Path $Tmp $file.filename
  if (-not (Test-Path -LiteralPath $whl)) {
    $mb = [math]::Round($file.size / 1MB)
    Write-Info ("downloading {0} ({1} MB) ..." -f $file.filename, $mb)
    & curl.exe -fL --retry 3 --retry-delay 2 -o $whl $file.url
    if ($LASTEXITCODE -ne 0) {
      Write-Host "[CUDA] ERROR: download failed: $($file.url)" -ForegroundColor Red
      exit 1
    }
  } else {
    Write-Info "cached: $($file.filename)"
  }

  Write-Info "unpacking $($file.filename) ..."
  $ex = Join-Path $Tmp ("x-" + ($pkg -replace '[^a-zA-Z0-9]', '_'))
  if (Test-Path -LiteralPath $ex) { Remove-Item -Recurse -Force $ex }
  Expand-Archive -LiteralPath $whl -DestinationPath $ex -Force

  # wheels keep the payload under nvidia\<pkg>\bin\*.dll
  Get-ChildItem -Path $ex -Recurse -Filter '*.dll' | ForEach-Object {
    Copy-Item -Force $_.FullName $Dest
  }
}

Remove-Item -Recurse -Force $Tmp -ErrorAction SilentlyContinue

Write-Info "done. Libraries in third_party\cuda12:"
Get-ChildItem $Dest -Filter '*.dll' -ErrorAction SilentlyContinue |
  Where-Object { $_.Name -match 'cudart|cublas|cudnn64|cufft' } |
  ForEach-Object { Write-Host ("  {0,-34} {1,12:N0} bytes" -f $_.Name, $_.Length) }

@"

[CUDA] Next steps:
  build.cmd              (if the service binary is not built yet)
  start-laya.cmd         (default: device=cuda)
  start-laya.cmd /cpu    (explicit CPU run -- no CUDA needed at all)

If the engine reports that the CUDA provider failed to load, check that the
names above include cudart64_12.dll, cublas64_12.dll, cublasLt64_12.dll,
cudnn64_9.dll and cufft64_11.dll.
"@ | Write-Host
