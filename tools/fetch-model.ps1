# fetch-model.ps1 — ОДНОРАЗОВАЯ загрузка весов и словаря в models\.
#
# Обычный запуск (start-laya.cmd) ничего не скачивает: он только читает файлы из
# models\. Этот скрипт нужен ровно в одном случае — когда файлов нет (первая
# установка или восстановление после потери диска). После загрузки он проверяет
# SHA-256 по манифесту и говорит, совпало ли.
#
# Запуск:  powershell -ExecutionPolicy Bypass -File tools\fetch-model.ps1
#          ... -Force   — перекачать даже если файл уже есть

param(
  [string]$Root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path,
  [switch]$Force
)

$ErrorActionPreference = 'Stop'

# Хеш через .NET: Get-FileHash доступен не во всех окружениях.
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

$sources = @(
  @{
    name = 'multilingual-fp16.onnx'
    url  = 'https://huggingface.co/mizchi/laya-multilingual-onnx/resolve/main/model.onnx'
    dest = 'models\multilingual-fp16.onnx'
    note = 'веса fp16, 322M (рабочая модель)'
  },
  @{
    name = 'tokenizer.json'
    url  = 'https://huggingface.co/techtheist/laya-onnx/resolve/main/multilingual/tokenizer.json'
    dest = 'models\tokenizer.json'
    note = 'словарь mmBERT, 256000 токенов'
  }
)

$manifestPath = Join-Path $Root 'models\model-manifest.json'
$manifest = $null
if (Test-Path $manifestPath) {
  $manifest = Get-Content -Raw -LiteralPath $manifestPath | ConvertFrom-Json
}

$failed = @()

foreach ($src in $sources) {
  $dest = Join-Path $Root $src.dest
  $dir = Split-Path -Parent $dest
  if (-not (Test-Path $dir)) { New-Item -ItemType Directory -Force -Path $dir | Out-Null }

  if ((Test-Path -LiteralPath $dest) -and -not $Force) {
    Write-Host ("[FETCH] {0} уже есть — пропускаю (нужно перекачать: -Force)" -f $src.name) -ForegroundColor Yellow
    continue
  }

  Write-Host ("[FETCH] качаю {0} ({1})" -f $src.name, $src.note) -ForegroundColor Cyan
  Write-Host ("[FETCH]   {0}" -f $src.url)

  $tmp = "$dest.part"
  if (Test-Path -LiteralPath $tmp) { Remove-Item -LiteralPath $tmp -Force }

  # curl.exe есть в Windows 10+: он умеет докачку и не грузит файл в память.
  $curl = Get-Command curl.exe -ErrorAction SilentlyContinue
  if ($curl) {
    & curl.exe -L --fail --retry 3 --retry-delay 2 -o $tmp $src.url
    if ($LASTEXITCODE -ne 0) {
      Write-Host ("[FETCH] curl завершился с кодом {0}" -f $LASTEXITCODE) -ForegroundColor Red
      $failed += $src.name
      continue
    }
  } else {
    try {
      Invoke-WebRequest -Uri $src.url -OutFile $tmp -UseBasicParsing
    } catch {
      Write-Host ("[FETCH] ошибка загрузки: {0}" -f $_.Exception.Message) -ForegroundColor Red
      $failed += $src.name
      continue
    }
  }

  Move-Item -LiteralPath $tmp -Destination $dest -Force
  $size = (Get-Item -LiteralPath $dest).Length
  Write-Host ("[FETCH]   сохранено: {0:N0} байт" -f $size)

  if ($manifest -and $manifest.files.$($src.name)) {
    $want = $manifest.files.$($src.name)
    if ($size -ne $want.bytes) {
      Write-Host ("[FETCH]   ВНИМАНИЕ: размер отличается от манифеста ({0:N0} вместо {1:N0})" -f $size, $want.bytes) -ForegroundColor Yellow
    }
    $hash = Get-Sha256Hex $dest
    if ($hash -eq $want.sha256) {
      Write-Host "[FETCH]   SHA-256 совпал с манифестом" -ForegroundColor Green
    } else {
      Write-Host "[FETCH]   SHA-256 НЕ совпал с манифестом:" -ForegroundColor Yellow
      Write-Host ("           ожидался {0}" -f $want.sha256)
      Write-Host ("           получен  {0}" -f $hash)
      Write-Host "           Источник на Hugging Face мог обновиться или файл повреждён."
      Write-Host "           Если ответы модели выглядят странно — обновите models\model-manifest.json."
    }
  }
}

Write-Host ""
if ($failed.Count -gt 0) {
  Write-Host ("[FETCH] не удалось скачать: {0}" -f ($failed -join ', ')) -ForegroundColor Red
  exit 1
}
Write-Host "[FETCH] готово. Полная проверка:  verify-model.cmd"
exit 0
