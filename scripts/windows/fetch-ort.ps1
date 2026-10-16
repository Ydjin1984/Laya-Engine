# ===========================================================================
# fetch-ort.ps1 -- download ONNX Runtime 1.25.0 for Windows and unpack it.
#
# Why exactly 1.25.0: the engine is built against this release's C API and the
# checkpoint was calibrated on it (see docs/ENGINE.ru.md).
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File scripts\windows\fetch-ort.ps1
#   ... -Cpu          # CPU-only build (no CUDA execution provider, ~80 MB)
#   ... -Force        # re-download even if already unpacked
#
# Result: third_party\onnxruntime-win-x64[-gpu]-1.25.0\  (+ copies in bin\)
# ===========================================================================
param(
  [switch]$Cpu,
  [switch]$Force,
  [string]$Version = '1.25.0'
)

$ErrorActionPreference = 'Stop'
$Root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path

if ($env:PROCESSOR_ARCHITECTURE -ne 'AMD64') {
  Write-Host "[ORT] ERROR: this script expects 64-bit Windows (x64)." -ForegroundColor Red
  exit 1
}

$flavor = if ($Cpu) { '' } else { '-gpu' }
$pkg = "onnxruntime-win-x64$flavor-$Version"
$url = "https://github.com/microsoft/onnxruntime/releases/download/v$Version/$pkg.zip"
$dest = Join-Path $Root "third_party\$pkg"
$zip = Join-Path $Root "third_party\$pkg.zip"

Write-Host "[ORT] package : $pkg"
Write-Host "[ORT] url     : $url"

function Get-File($Url, $Out) {
  # curl.exe ships with Windows 10+; fall back to .NET for older systems.
  $curl = Get-Command curl.exe -ErrorAction SilentlyContinue
  if ($curl) {
    & curl.exe -fL --retry 3 --retry-delay 2 -o $Out $Url
    if ($LASTEXITCODE -ne 0) { throw "curl failed with exit code $LASTEXITCODE" }
  } else {
    $old = $ProgressPreference; $ProgressPreference = 'SilentlyContinue'
    Invoke-WebRequest -Uri $Url -OutFile $Out
    $ProgressPreference = $old
  }
}

if ((Test-Path -LiteralPath $dest) -and -not $Force) {
  Write-Host "[ORT] already unpacked: $dest"
} else {
  New-Item -ItemType Directory -Force -Path (Join-Path $Root 'third_party') | Out-Null
  Write-Host "[ORT] downloading ..."
  Get-File $url $zip
  Write-Host "[ORT] unpacking ..."
  if (Test-Path -LiteralPath $dest) { Remove-Item -Recurse -Force $dest }
  Expand-Archive -LiteralPath $zip -DestinationPath (Join-Path $Root 'third_party') -Force
  Remove-Item -Force $zip
}

# Copy the DLLs the engine loads next to the binary (default layout).
$bin = Join-Path $Root 'bin'
New-Item -ItemType Directory -Force -Path $bin | Out-Null
$lib = Join-Path $dest 'lib'
foreach ($name in @('onnxruntime.dll', 'onnxruntime_providers_shared.dll')) {
  $src = Join-Path $lib $name
  if (Test-Path -LiteralPath $src) { Copy-Item -Force $src $bin }
}
if (-not $Cpu) {
  $src = Join-Path $lib 'onnxruntime_providers_cuda.dll'
  if (Test-Path -LiteralPath $src) { Copy-Item -Force $src $bin }
  Write-Host "[ORT] CUDA execution provider included. Next: scripts\windows\fetch-cuda.ps1"
}

Write-Host "[ORT] done:" -ForegroundColor Green
Get-ChildItem $bin -Filter 'onnxruntime*.dll' | ForEach-Object {
  Write-Host ("  {0,-42} {1,10:N0} bytes" -f $_.Name, $_.Length)
}
Write-Host "[ORT] next: build.cmd (if Go + gcc are installed) or scripts\windows\install.ps1 -Prebuilt"
