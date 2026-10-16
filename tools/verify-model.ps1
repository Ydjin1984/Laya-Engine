# verify-model.ps1 — проверка ЛОКАЛЬНЫХ файлов модели и рантайма по манифесту.
#
# Ничего не скачивается: скрипт отвечает на вопрос «на месте ли веса и можно ли
# винить в сбое файл, а не код». Сравниваются размер и SHA-256.
#
# Запуск:  powershell -ExecutionPolicy Bypass -File tools\verify-model.ps1
#          ... -SkipHash   — только наличие и размер (быстро, без чтения 1.5 ГБ)

param(
  [string]$Root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path,
  [switch]$SkipHash
)

$ErrorActionPreference = 'Stop'
$manifestPath = Join-Path $Root 'models\model-manifest.json'

# Хеш считается через .NET, а не через Get-FileHash: этот cmdlet есть не в каждом
# окружении (урезанные профили, ограниченные политики), и тогда проверка падала
# бы целиком, показывая ложное "BAD HASH" на исправном файле.
function Get-Sha256Hex([string]$Path) {
  $sha = [System.Security.Cryptography.SHA256]::Create()
  try {
    $stream = [System.IO.File]::OpenRead($Path)
    try {
      $bytes = $sha.ComputeHash($stream)
      return ([System.BitConverter]::ToString($bytes) -replace '-', '').ToLower()
    } finally { $stream.Dispose() }
  } finally { $sha.Dispose() }
}

if (-not (Test-Path $manifestPath)) {
  Write-Host "[MODEL] нет манифеста: $manifestPath" -ForegroundColor Red
  exit 1
}

$manifest = Get-Content -Raw -LiteralPath $manifestPath | ConvertFrom-Json
$bad = @()
$total = 0.0

Write-Host "[MODEL] корень проекта: $Root"
Write-Host "[MODEL] манифест: $manifestPath"
Write-Host ""

foreach ($prop in $manifest.files.PSObject.Properties) {
  $name = $prop.Name
  $entry = $prop.Value
  $file = Join-Path $Root $entry.path
  $total += ($entry.bytes / 1MB)

  if (-not (Test-Path -LiteralPath $file)) {
    Write-Host ("  MISSING  {0,-38} ожидался {1}" -f $name, $entry.path) -ForegroundColor Red
    $bad += $name
    continue
  }

  $item = Get-Item -LiteralPath $file
  if ($item.Length -ne $entry.bytes) {
    Write-Host ("  BAD SIZE {0,-38} {1:N0} вместо {2:N0}" -f $name, $item.Length, $entry.bytes) -ForegroundColor Red
    $bad += $name
    continue
  }

  if ($SkipHash) {
    Write-Host ("  OK       {0,-38} {1:N0} байт (хеш не проверялся)" -f $name, $item.Length) -ForegroundColor Green
    continue
  }

  $hash = Get-Sha256Hex $file
  if ($hash -ne $entry.sha256) {
    Write-Host ("  BAD HASH {0,-38}" -f $name) -ForegroundColor Red
    Write-Host ("           ожидался {0}" -f $entry.sha256)
    Write-Host ("           получен  {0}" -f $hash)
    $bad += $name
    continue
  }
  Write-Host ("  OK       {0,-38} {1:N0} байт  {2}..." -f $name, $item.Length, $hash.Substring(0, 16)) -ForegroundColor Green
}

Write-Host ""
if ($bad.Count -gt 0) {
  Write-Host ("[MODEL] проблемные файлы: {0}" -f ($bad -join ', ')) -ForegroundColor Red
  Write-Host "[MODEL] сервис с ними не запустится или будет считать неправильно."
  Write-Host "[MODEL] восстановить:  fetch-model.cmd  (скачает отсутствующее один раз)"
  exit 1
}

Write-Host ("[MODEL] всё на месте ({0:N1} МБ). Старт сервису ничего скачивать не нужно." -f $total) -ForegroundColor Green
exit 0
