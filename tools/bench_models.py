"""Замер скорости моделей, между которыми будет выбирать Laya.

Сравнивает локальный мозг Киборга (27B на этой машине) с облачными моделями,
которые уже прописаны в DSH. Замеряет время до первого токена и полное время —
именно они определяют, почувствует ли пользователь разницу.

Ключи берутся из ~/.dsh/.credentials.yaml и НИКОГДА не печатаются.

Запуск: python tools/bench_models.py [--prompt-tokens 2000]
"""
import os
import argparse
import json
import re
import sys
import time
import urllib.error
import urllib.request

# Корень репозитория: пути считаются от расположения скрипта, а не от машины.
_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

CREDS = r"C:\Users\lex66\.dsh\.credentials.yaml"
LOCAL = "http://127.0.0.1:8091/v1/chat/completions"

# Кандидаты: (подпись, url, модель, имя ключа)
CANDIDATES = [
    ("локальный 27B (эта машина)", LOCAL, "Kibborg_v5.8_MAX", None),
    ("OpenRouter qwen3.8-27b (free)", "https://openrouter.ai/api/v1/chat/completions",
     "qwen/qwen3.8-27b:free", "OPENROUTER_API_KEY"),
    ("OpenRouter nemotron-lightning (free)", "https://openrouter.ai/api/v1/chat/completions",
     "nvidia/nemotron-3.5-lightning:free", "OPENROUTER_API_KEY"),
    ("DeepSeek V4 Flash", "https://api.deepseek.com/chat/completions",
     "deepseek-flash", "DEEPSEEK_API_KEY"),
    ("Kimi K2.7 Code HighSpeed", "https://api.moonshot.ai/v1/chat/completions",
     "kimi-k2.7-code-highspeed", "KIMI_API_KEY"),
]

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass


def load_keys() -> dict:
    keys = {}
    try:
        with open(CREDS, encoding="utf-8") as f:
            for line in f:
                m = re.match(r"\s*([A-Z0-9_]+)\s*:\s*(\S+)\s*$", line)
                if m:
                    keys[m.group(1)] = m.group(2).strip().strip('"').strip("'")
    except FileNotFoundError:
        pass
    return keys


def build_prompt(target_tokens: int) -> tuple[str, str]:
    """Системный промпт с инструментами + вопрос: похоже на реальный запрос DSH."""
    tool = ('{"type":"function","function":{"name":"run_command","description":"Запустить '
            'команду в терминале и вернуть вывод целиком","parameters":{"type":"object",'
            '"properties":{"command":{"type":"string","description":"команда для запуска"}},'
            '"required":["command"]}}}')
    tools = "[" + ",".join(tool for _ in range(12)) + "]"
    body = ("Ты агент с инструментами. Ниже список инструментов.\n" + tools + "\n")
    filler = ("Правило работы: сначала уточняй цель, затем действуй, проверяй результат, "
              "не выдумывай факты, сообщай о провале прямо. ")
    while len(body) < target_tokens * 3:
        body += filler
    return body, "Скажи одним словом: ок"


def call(url: str, model: str, key: str | None, system: str, user: str, max_tokens: int = 64):
    payload = {
        "model": model,
        "messages": [{"role": "system", "content": system}, {"role": "user", "content": user}],
        "max_tokens": max_tokens,
        "stream": True,
    }
    headers = {"Content-Type": "application/json"}
    if key:
        headers["Authorization"] = "Bearer " + key
    req = urllib.request.Request(url, data=json.dumps(payload).encode(), headers=headers)
    started = time.perf_counter()
    ttft = None
    first = ""
    try:
        with urllib.request.urlopen(req, timeout=300) as r:
            for raw in r:
                line = raw.decode("utf-8", "replace").strip()
                if not line.startswith("data:"):
                    continue
                data = line[5:].strip()
                if data == "[DONE]":
                    break
                try:
                    ev = json.loads(data)
                except json.JSONDecodeError:
                    continue
                delta = (ev.get("choices") or [{}])[0].get("delta", {})
                piece = delta.get("content") or ""
                if piece and ttft is None:
                    ttft = time.perf_counter() - started
                    first = piece
        total = time.perf_counter() - started
        return {"ok": True, "ttft_s": ttft or total, "total_s": total, "first": first[:40]}
    except urllib.error.HTTPError as e:
        return {"ok": False, "error": f"http {e.code}: {e.read()[:120].decode('utf-8', 'replace')}"}
    except Exception as e:  # noqa: BLE001
        return {"ok": False, "error": str(e)[:120]}


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--prompt-tokens", type=int, default=2000)
    ap.add_argument("--repeat", type=int, default=2)
    args = ap.parse_args()

    keys = load_keys()
    system, user = build_prompt(args.prompt_tokens)
    print(f"промпт ≈{args.prompt_tokens} токенов, повторов на модель: {args.repeat}\n")
    print(f"{'модель':<38}{'до 1-го токена':>16}{'всего':>10}  ответ")
    print("-" * 90)

    rows = []
    for label, url, model, keyname in CANDIDATES:
        key = keys.get(keyname) if keyname else None
        if keyname and not key:
            print(f"{label:<38}{'—':>16}{'—':>10}  ключа нет")
            continue
        best_ttft, best_total, sample, err = None, None, "", ""
        for _ in range(args.repeat):
            r = call(url, model, key, system, user)
            if not r["ok"]:
                err = r["error"]
                continue
            if best_ttft is None or r["ttft_s"] < best_ttft:
                best_ttft, best_total, sample = r["ttft_s"], r["total_s"], r["first"]
        if best_ttft is None:
            print(f"{label:<38}{'—':>16}{'—':>10}  {err}")
            rows.append({"label": label, "error": err})
            continue
        print(f"{label:<38}{best_ttft:>14.2f}с{best_total:>8.2f}с  {sample!r}")
        rows.append({"label": label, "ttft_s": best_ttft, "total_s": best_total})

    out = os.path.join(_ROOT, "tools", "model_bench.json")
    with open(out, "w", encoding="utf-8") as f:
        json.dump({"prompt_tokens": args.prompt_tokens, "rows": rows}, f, ensure_ascii=False, indent=1)
    print(f"\nподробности: {out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
