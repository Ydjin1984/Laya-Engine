# ===========================================================================
# powershell-example.ps1 -- minimal Laya Engine client for Windows.
#
# Run:  powershell -ExecutionPolicy Bypass -File examples\powershell-example.ps1
#       ... -Base http://127.0.0.1:8095
# ===========================================================================
param(
  [string]$Base = 'http://127.0.0.1:8095'
)

$ErrorActionPreference = 'Stop'

# A system HTTP proxy (Windows "Internet Settings") must not be used for a
# loopback service: Force the request to go direct.
function Invoke-LayaPost([string]$Path, [hashtable]$Payload) {
  $json = $Payload | ConvertTo-Json -Depth 10 -Compress
  $bytes = [Text.Encoding]::UTF8.GetBytes($json)
  return Invoke-RestMethod -Uri "$Base$Path" -Method Post -Body $bytes `
    -ContentType 'application/json; charset=utf-8' -Proxy $null
}

function Get-Laya([string]$Path) {
  return Invoke-RestMethod -Uri "$Base$Path" -Method Get -Proxy $null
}

# --- readiness ----------------------------------------------------------------
$deadline = (Get-Date).AddSeconds(180)
while ($true) {
  try { Get-Laya '/ready' | Out-Null; break }
  catch {
    if ((Get-Date) -gt $deadline) { throw "engine is not ready -- start it first (start-laya.cmd)" }
    Start-Sleep -Seconds 1
  }
}

$info = Get-Laya '/info'
$cp = $info.checkpoint
Write-Host ("engine: device={0} checkpoint={1}-{2}" -f $info.device, $cp.file, $cp.sha256.Substring(0, 12))

$state = 'удали файл D:\tmp\old.txt и покажи список'

# Wording matters on a 322M model: `noul` takes a full, concrete statement
# rather than a short abstract query. See docs/API.md.
$questions = @{
  route = @{
    type         = 'choice'
    instructions = 'Какой набор инструментов нужен?'
    criteria     = @{
      dialog      = 'просто ответить словами'
      files_write = 'изменить или удалить файл на диске'
      console     = 'выполнить команду в терминале'
    }
  }
  risk = @{
    type         = 'score'
    instructions = 'Оцени риск действия по шкале.'
    criteria     = @('низкий', 'средний', 'высокий')
  }
  data_loss = @{
    type         = 'noul'
    instructions = 'Уничтожит ли это действие несохранённые или невосстановимые данные пользователя (сброс изменений, удаление файлов, перезапись без копии)?'
  }
}

# --- 1. budget without inference ----------------------------------------------
$count = Invoke-LayaPost '/count' @{ state = $state; questions = $questions }
Write-Host ("budget: state_tokens={0} fits={1}" -f $count.state_tokens, $count.fits)

# --- 2. one decide call (one forward pass for all three questions) ------------
$res = Invoke-LayaPost '/decide' @{ state = $state; questions = $questions; max_len = 1024 }
Write-Host ("checkpoint={0}  elapsed={1:N1} ms  input_tokens={2}" -f $res.checkpoint, $res.elapsed_ms, $res.usage.input_tokens)

$usable = $true
if ($res.budget.state_truncated) {
  $usable = $false
  Write-Host 'WARNING: state truncated -- the model did not see the tail; do not trust the answer blindly.' -ForegroundColor Yellow
}

foreach ($qid in $res.order) {
  $a = $res.answers.$qid
  switch ($a.type) {
    'choice' {
      Write-Host ("{0}: choice={1}  answer_confidence={2:N3}" -f $qid, $a.choice, $a.answer_confidence)
    }
    'score' {
      Write-Host ("{0}: score={1:N2} (0..{2})  answer_confidence={3:N3}" -f $qid, $a.score, ($a.probabilities.Count - 1), $a.answer_confidence)
    }
    default {
      Write-Host ("{0}: noul={1:N3}  answer_confidence={2:N3}" -f $qid, $a.noul, $a.answer_confidence)
    }
  }
  if ($a.collapsed) { $usable = $false; Write-Host ("  WARNING: options collapsed ({0}) -- unreliable." -f $qid) -ForegroundColor Yellow }
}

Write-Host ("result: " + $(if ($usable) { 'usable' } else { 'DEGRADED (see warnings)' }))
