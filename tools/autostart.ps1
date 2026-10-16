# autostart.ps1 — регистрация Laya-сервиса в планировщике заданий Windows.
#
# Задача стартует при ЗАГРУЗКЕ машины (не при входе пользователя): триггер
# AtStartup, учётная запись SYSTEM. Пароль не нужен, сервис поднимается до
# логина и доступен по loopback сразу после включения.
#
# 30 секунд задержки: триггер AtStartup срабатывает очень рано, и без паузы
# задание может обогнать загрузку драйвера NVIDIA.
#
# Запуск:  powershell -ExecutionPolicy Bypass -File tools\autostart.ps1
#          ... -Remove   — снять задачу
#
# Обычно вызывается через install-autostart.cmd / uninstall-autostart.cmd.

param(
  [string]$Root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path,
  [string]$TaskName = 'LayaEngineServer',
  [int]$DelaySeconds = 30,
  [string]$Port = '8095',
  [int]$DeviceId = 0,
  [switch]$Remove
)

$ErrorActionPreference = 'Stop'

function Fail([string]$Message) {
  Write-Host "[LAYA] ERROR: $Message" -ForegroundColor Red
  exit 1
}

# --------------------------------------------------------------- снятие задачи
if ($Remove) {
  $existing = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
  if (-not $existing) {
    Write-Host "[LAYA] задача '$TaskName' не зарегистрирована - нечего снимать."
    exit 0
  }
  Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
  Write-Host "[LAYA] задача '$TaskName' снята."
  exit 0
}

# ------------------------------------------------------- проверки перед стартом
# Задание, зарегистрированное на несуществующий путь, падает молча и только
# при следующей перезагрузке - поэтому всё проверяется здесь, руками.
$exe = Join-Path $Root 'bin\laya-server.exe'
$ort = Join-Path $Root 'bin\onnxruntime.dll'
$model = Join-Path $Root 'models\multilingual-fp16.onnx'
$tokenizer = Join-Path $Root 'models\tokenizer.json'
$cudaDir = Join-Path $Root 'third_party\cuda12'
$logDir = Join-Path $Root 'logs'
$log = Join-Path $logDir 'laya-server.log'

foreach ($f in @($exe, $ort, $model, $tokenizer)) {
  if (-not (Test-Path -LiteralPath $f)) { Fail "не найден файл: $f" }
}
if (-not (Test-Path -LiteralPath (Join-Path $cudaDir 'cudart64_12.dll'))) {
  Fail "нет библиотек CUDA 12 в $cudaDir"
}
if (-not (Test-Path -LiteralPath $logDir)) {
  New-Item -ItemType Directory -Path $logDir -Force | Out-Null
}

# --------------------------------------------------------------- сама регистрация
# Пути передаются как есть: пробелов в E:\Laya_Engine нет, поэтому кавычки
# внутри строки аргументов не нужны и только усложнили бы разбор.
$arguments = @(
  '-device cuda'
  "-device-id $DeviceId"
  "-model $model"
  "-tokenizer $tokenizer"
  "-ort $ort"
  "-addr 127.0.0.1:$Port"
  "-log $log"
  '-warmup=true'
) -join ' '

$action = New-ScheduledTaskAction -Execute $exe -Argument $arguments -WorkingDirectory $Root

$trigger = New-ScheduledTaskTrigger -AtStartup
# Свойство Delay есть не во всех сборках PowerShell - если его нет, задача
# просто стартует чуть раньше, это не повод не регистрировать её.
try { $trigger.Delay = "PT${DelaySeconds}S" } catch {
  Write-Host "[LAYA] WARN: не удалось выставить задержку триггера - старт без паузы."
}

# S-1-5-18 = SYSTEM. SID, а не имя: имя локализовано, SID - нет.
$principal = New-ScheduledTaskPrincipal -UserId 'S-1-5-18' -LogonType ServiceAccount -RunLevel Highest

$settings = New-ScheduledTaskSettingsSet `
  -AllowStartIfOnBatteries `
  -DontStopIfGoingOnBatteries `
  -StartWhenAvailable `
  -MultipleInstances IgnoreNew `
  -ExecutionTimeLimit ([TimeSpan]::Zero) `
  -RestartInterval (New-TimeSpan -Minutes 1) `
  -RestartCount 3

$description = "Laya decision service (ONNX Runtime CUDA, GTX 1650 = GPU #$DeviceId). " +
  "Starts at machine boot on 127.0.0.1:$Port; model and weights stay local."

Register-ScheduledTask `
  -TaskName $TaskName `
  -Action $action `
  -Trigger $trigger `
  -Principal $principal `
  -Settings $settings `
  -Description $description `
  -Force | Out-Null

Write-Host "[LAYA] задача '$TaskName' зарегистрирована."
Write-Host "[LAYA]   команда:   $exe"
Write-Host "[LAYA]   аргументы: $arguments"
Write-Host "[LAYA]   триггер:   при загрузке машины, задержка ${DelaySeconds}s, от имени SYSTEM"
Write-Host "[LAYA]   лог:       $log"
Write-Host "[LAYA] проверить сейчас:  schtasks /run /tn $TaskName"
Write-Host "[LAYA] состояние:         schtasks /query /tn $TaskName /v /fo list"
