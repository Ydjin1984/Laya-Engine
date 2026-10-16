#!/usr/bin/env bash
# ===========================================================================
# curl-examples.sh -- end-to-end smoke test of the local Laya Engine API.
#
# Usage:  bash examples/curl-examples.sh [http://127.0.0.1:8095]
# Needs:  curl (+ python3 for pretty-printing, optional)
# ===========================================================================
set -euo pipefail

BASE="${1:-${LAYA_BASE:-http://127.0.0.1:8095}}"
HDR=(-H 'Content-Type: application/json')

pretty() { if command -v python3 >/dev/null 2>&1; then python3 -m json.tool; else cat; fi; }

echo "=== 1. /health -- process alive (model may still be loading) ==="
curl -fsS "$BASE/health"; echo

echo
echo "=== 2. /ready -- model loaded and answering ==="
curl -fsS "$BASE/ready"; echo

echo
echo "=== 3. /info -- schema, device, checkpoint, counters ==="
curl -fsS "$BASE/info" | pretty | head -40

STATE='удали файл D:\tmp\old.txt и покажи список'

echo
echo "=== 4. /count -- budgets only, NO inference (tokenization, ~1 ms) ==="
curl -fsS "$BASE/count" "${HDR[@]}" -d @- <<'JSON' | pretty
{
  "state": "удали файл D:\\tmp\\old.txt и покажи список",
  "questions": {
    "route": {
      "type": "choice",
      "instructions": "Какой набор инструментов нужен?",
      "criteria": {
        "dialog": "просто ответить словами",
        "files_write": "изменить или удалить файл на диске",
        "console": "выполнить команду в терминале"
      }
    }
  }
}
JSON

echo
echo "=== 5. /decide -- one batch, three question types in one forward pass ==="
curl -fsS "$BASE/decide" "${HDR[@]}" -d @- <<'JSON' | pretty
{
  "state": "удали файл D:\\tmp\\old.txt и покажи список",
  "questions": {
    "route": {
      "type": "choice",
      "instructions": "Какой набор инструментов нужен?",
      "criteria": {
        "dialog": "просто ответить словами",
        "files_write": "изменить или удалить файл на диске",
        "console": "выполнить команду в терминале"
      }
    },
    "risk": {
      "type": "score",
      "instructions": "Оцени риск действия по шкале.",
      "criteria": ["низкий", "средний", "высокий"]
    },
    "data_loss": {
      "type": "noul",
      "instructions": "Уничтожит ли это действие несохранённые или невосстановимые данные пользователя (сброс изменений, удаление файлов, перезапись без копии)?"
    }
  },
  "max_len": 1024
}
JSON

echo
echo "=== 6. /benchmark -- p50/p95/p99 on this machine ==="
curl -fsS "$BASE/benchmark" "${HDR[@]}" -d @- <<'JSON' | pretty
{
  "state": "разбери BTC по Герчику",
  "questions": {
    "route": {
      "type": "choice",
      "instructions": "Какой маршрут обработки?",
      "criteria": { "fast": "простой запрос", "deep": "нужен анализ" }
    }
  },
  "iterations": 20,
  "concurrency": 1
}
JSON

echo
echo "=== done. Note: /decide answers carry 'checkpoint' -- bind your thresholds to it. ==="
