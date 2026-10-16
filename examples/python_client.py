#!/usr/bin/env python3
"""Minimal Laya Engine client (Python 3, standard library only).

Shows the intended usage pattern:
  1. wait for readiness;
  2. ask /count BEFORE deciding (budget check without running the model);
  3. batch independent questions into ONE /decide call (one forward pass);
  4. treat the answer as a probability, not as proof: bind thresholds to
     `answer_confidence` and check `budget.state_truncated` / `collapsed`.

Run:  python3 examples/python_client.py [http://127.0.0.1:8095]
"""
from __future__ import annotations

import json
import sys
import time
import urllib.error
import urllib.request

BASE = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:8095"

# Local service: never route 127.0.0.1 through a system HTTP proxy. On Windows
# urllib picks the proxy up from the registry, and a proxy that does not accept
# loopback makes every request fail with "Remote end closed connection".
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def post(path: str, payload: dict, timeout: float = 30.0) -> dict:
    req = urllib.request.Request(
        BASE + path,
        data=json.dumps(payload, ensure_ascii=False).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with OPENER.open(req, timeout=timeout) as r:
        return json.load(r)


def get(path: str, timeout: float = 5.0) -> dict:
    with OPENER.open(BASE + path, timeout=timeout) as r:
        return json.load(r)


def wait_ready(seconds: float = 180.0) -> None:
    deadline = time.time() + seconds
    while time.time() < deadline:
        try:
            get("/ready", timeout=2.0)
            return
        except (urllib.error.URLError, TimeoutError):
            time.sleep(1.0)
    raise SystemExit("engine is not ready -- start it first (scripts/laya.sh start / start-laya.cmd)")


STATE = "удали файл D:\\tmp\\old.txt и покажи список"

# Wording matters on a 322M model: `noul` answers take a full, concrete
# statement ("will this action destroy unrecoverable data") far better than a
# short abstract query about "the request". These phrasings return meaningful
# values on this checkpoint; see docs/API.md ("Формулировки вопросов").
QUESTIONS = {
    # choice: pick one of the criteria
    "route": {
        "type": "choice",
        "instructions": "Какой набор инструментов нужен?",
        "criteria": {
            "dialog": "просто ответить словами",
            "files_write": "изменить или удалить файл на диске",
            "console": "выполнить команду в терминале",
        },
    },
    # score: rank on an ordered scale (low -> high)
    "risk": {
        "type": "score",
        "instructions": "Оцени риск действия по шкале.",
        "criteria": ["низкий", "средний", "высокий"],
    },
    # noul: P(statement is true), stated as a concrete claim
    "data_loss": {
        "type": "noul",
        "instructions": "Уничтожит ли это действие несохранённые или "
                        "невосстановимые данные пользователя (сброс изменений, "
                        "удаление файлов, перезапись без копии)?",
    },
}


def main() -> None:
    wait_ready()
    info = get("/info")
    cp = info.get("checkpoint", {})
    # The short checkpoint id used by clients is file + first 12 hex of sha256;
    # /decide returns it directly as `checkpoint`.
    cp_id = f"{cp.get('file', '?')}-{(cp.get('sha256') or '')[:12]}"
    print(f"engine: device={info.get('device')} checkpoint={cp_id}")

    # 1) budget check -- tokenization only, no model run (~1 ms)
    count = post("/count", {"state": STATE, "questions": QUESTIONS})
    print(f"budget: state_tokens={count['state_tokens']} fits={count['fits']}")
    for q in count["questions"]:
        print(f"  question: options={q['options']} head_tokens={q['head_tokens']} "
              f"state_dropped={q['state_dropped']} fits={q['fits']}")

    # 2) one decide call for all three questions
    res = post("/decide", {"state": STATE, "questions": QUESTIONS, "max_len": 1024})
    print(f"\ncheckpoint={res['checkpoint']}  elapsed={res['elapsed_ms']:.1f} ms  "
          f"input_tokens={res['usage']['input_tokens']}")

    ok = True
    budget = res["budget"]
    if budget.get("state_truncated"):
        ok = False
        print("WARNING: the state was truncated -- the model did not see the tail; "
              "do not let this answer drive decisions.")

    for qid in res["order"]:
        a = res["answers"][qid]
        if a["type"] == "choice":
            top = sorted(a["probabilities"], key=lambda p: -p["value"])[:3]
            shown = ", ".join(f"{p['label']}={p['value']:.3f}" for p in top)
            print(f"{qid:<8} choice={a['choice']:<12} answer_confidence={a['answer_confidence']:.3f}  [{shown}]")
        elif a["type"] == "score":
            print(f"{qid:<8} score={a['score']:.2f} (0..{len(a['probabilities']) - 1}) "
                  f"answer_confidence={a['answer_confidence']:.3f}")
        else:
            print(f"{qid:<8} noul={a['noul']:.3f} answer_confidence={a['answer_confidence']:.3f}")
        if a.get("collapsed"):
            ok = False
            print(f"  WARNING: options collapsed after truncation -- the answer is unreliable")
    print("\nresult:", "usable" if ok else "DEGRADED (see warnings)")


if __name__ == "__main__":
    main()
