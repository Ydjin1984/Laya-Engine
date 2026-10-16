"""Контрольный тест пайплайна: пример из документации Laya.

Если на примере из README модель отвечает `billing` (а не равномерным
распределением), значит сборка последовательности и декодирование верны, и
провал на русских запросах — свойство модели, а не ошибка препроцессинга.
"""
import os
import json
import numpy as np
import onnxruntime as ort
from tokenizers import Tokenizer

import sys
# Корень репозитория: пути считаются от расположения скрипта, а не от машины.
_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

sys.path.insert(0, os.path.join(_ROOT, "tools"))
from ref_decide import build_sequence, entropy_conf  # noqa: E402

MODEL = sys.argv[1] if len(sys.argv) > 1 else os.path.join(_ROOT, "models", "multilingual-int8.onnx")
TOK = os.path.join(_ROOT, "models", "tokenizer.json")

state = "Hi, we were billed twice for March. Please refund the duplicate today or we will cancel our plan."
questions = {
    "department": {
        "type": "choice",
        "instructions": "Which department should handle this?",
        "criteria": {"billing": "invoices, payments, refunds",
                     "technical": "bugs, outages, system errors",
                     "other": "everything else"},
    },
    "urgency": {
        "type": "score",
        "instructions": "How urgent is this?",
        "criteria": ["not urgent", "soon", "blocking"],
    },
    "churn_risk": {
        "type": "noul",
        "instructions": "Does the user threaten to cancel or leave?",
    },
}

tok = Tokenizer.from_file(TOK)
sess = ort.InferenceSession(MODEL, providers=["CPUExecutionProvider"])
print("модель:", MODEL)

state_ids = tok.encode(state.replace("<mask>", " "), add_special_tokens=False).ids
items = []
for qid, q in questions.items():
    seq, markers, stats = build_sequence(tok, state, q, 1024, 256, state_ids)
    items.append({"ids": seq, "markers": markers, "qtype": {"choice": 0, "score": 1, "noul": 2}[q["type"]]})

n, L = len(items), max(len(it["ids"]) for it in items)
kmax = max(len(it["markers"]) for it in items)
ids = np.full((n, L), tok.token_to_id("<pad>"), dtype=np.int64)
att = np.zeros((n, L), dtype=np.int64)
mpos = np.zeros((n, kmax), dtype=np.int64)
mmask = np.zeros((n, kmax), dtype=bool)
qtype = np.array([it["qtype"] for it in items], dtype=np.int64)
for i, it in enumerate(items):
    ids[i, :len(it["ids"])] = it["ids"]
    att[i, :len(it["ids"])] = 1
    k = len(it["markers"])
    mpos[i, :k] = it["markers"]
    mmask[i, :k] = True

logits, act_logits = sess.run(["logits", "act_logits"], {
    "input_ids": ids, "attention_mask": att,
    "marker_pos": mpos, "marker_mask": mmask, "qtype": qtype})

print()
print("=== СЫРЫЕ ЛОГИТЫ ===")
for i, (qid, q) in enumerate(questions.items()):
    k = len(items[i]["markers"])
    print("  %-11s k=%d  логиты: %s" % (qid, k, np.round(logits[i, :k], 4)))
print("  act_logits:", np.round(act_logits, 4).tolist())

print()
print("=== ОТВЕТЫ ===")
for i, (qid, q) in enumerate(questions.items()):
    k = len(items[i]["markers"])
    z = logits[i, :k]
    p = np.exp(z - z.max())
    p = p / p.sum()
    if q["type"] == "choice":
        keys = list(q["criteria"].keys())
        print("  %-11s -> %-10s  вероятности: %s" % (qid, keys[int(p.argmax())],
              {kk: round(float(v), 4) for kk, v in zip(keys, p)}))
    elif q["type"] == "score":
        print("  %-11s -> уровень %.3f  вероятности: %s" % (qid, float((np.arange(k) * p).sum()),
              [round(float(v), 4) for v in p]))
    else:
        print("  %-11s -> P(да)=%.4f" % (qid, float(p[1])))

print()
print("разброс логитов внутри вопроса (max-min): %.4f" % max(
    float(np.ptp(logits[i, :len(items[i]["markers"])])) for i in range(len(items))))
