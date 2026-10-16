"""Диагностика: почему route accuracy 0.095.

Проверяет две гипотезы:
 1) варианты «схлопнулись» — 10 описаний не влезли в head_max_len=256 и обрезаны
    до неразличимых огрызков, значит модель физически не могла выбрать класс;
 2) латентность 1.8 с — следствие длины state (6 строк батча дублируют его).
"""
import os
import json
from collections import Counter

import numpy as np
from tokenizers import Tokenizer

# Корень репозитория: пути считаются от расположения скрипта, а не от машины.
_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

TOK = os.path.join(_ROOT, "models", "tokenizer.json")
CASES = os.path.join(_ROOT, "tools", "cases.json")
REF = os.path.join(_ROOT, "tools", "ref_answers.json")
HEAD_MAX_LEN = 256
MASK = "<mask>"

tok = Tokenizer.from_file(TOK)
with open(CASES, encoding="utf-8") as f:
    data = json.load(f)
with open(REF, encoding="utf-8") as f:
    ref = json.load(f)

q = data["questions"]["route"]
opts = [str(k) if v is None or v == "" else "%s: %s" % (k, v) for k, v in q["criteria"].items()]

print("=== БЛОК ВАРИАНТОВ (head_max_len=%d) ===" % HEAD_MAX_LEN)
head_ids = tok.encode("%s question: %s" % (q["type"], q["instructions"]), add_special_tokens=False).ids
print("заголовок вопроса: %d токенов" % len(head_ids))
opt_ids = []
for o in opts:
    body = " " + o.replace(MASK, " ")
    toks = tok.encode(body, add_special_tokens=False).ids[:48]
    opt_ids.append([tok.token_to_id(MASK)] + toks)
    print("  %-14s %3d токенов  %s" % (o.split(":")[0], len(toks) + 1, o[:70]))

total = sum(len(o) for o in opt_ids)
print("итого вариантов: %d токенов, бюджет %d" % (total, HEAD_MAX_LEN))
if HEAD_MAX_LEN - total < 16:
    per = max(4, (HEAD_MAX_LEN - 16) // len(opt_ids))
    print(">>> СРАБОТАЛА ОБРЕЗКА: каждый вариант урезан до %d токенов" % per)
    capped = [o[:per] for o in opt_ids]
    print(">>> различных вариантов после обрезки: %d из %d" % (len({tuple(o) for o in capped}), len(capped)))
    for o, c in zip(opt_ids, capped):
        print("    %-14s %d -> %d токенов" % (opts[opt_ids.index(o)].split(":")[0], len(o), len(c)))
else:
    print("обрезка не нужна, бюджет свободен: %d токенов" % (HEAD_MAX_LEN - total))

print()
print("=== ЧТО ПРЕДСКАЗЫВАЛА МОДЕЛЬ ===")
pred = Counter(r["answers"]["route"]["choice"] for r in ref["results"])
fact = Counter(r["fact_top"] for r in ref["results"])
print("%-14s %10s %10s" % ("класс", "предсказано", "фактически"))
for k in sorted(set(pred) | set(fact), key=lambda x: -fact.get(x, 0)):
    print("%-14s %10d %10d" % (k, pred.get(k, 0), fact.get(k, 0)))

print()
print("=== ТОЧНОСТЬ ПО КЛАССАМ ===")
for cls in sorted(fact, key=lambda x: -fact[x]):
    subset = [r for r in ref["results"] if r["fact_top"] == cls]
    hit = sum(1 for r in subset if r["answers"]["route"]["choice"] == cls)
    print("  %-14s %3d кейсов, верно %d (%.0f%%)" % (cls, len(subset), hit, 100.0 * hit / max(1, len(subset))))

conf = [r["answers"]["route"]["answer_confidence"] for r in ref["results"]]
print()
print("уверенность модели: медиана %.3f, p10 %.3f, p90 %.3f" % (np.median(conf), np.percentile(conf, 10), np.percentile(conf, 90)))
print("доля ответов с уверенностью >0.5: %.1f%%" % (100.0 * np.mean(np.array(conf) > 0.5)))
print("доля кейсов с длиной state >1500 символов: %.1f%%" % (
    100.0 * np.mean([len(c["state"]) > 1500 for c in data["cases"]])))
lens = [len(c["state"]) for c in data["cases"]]
print("длина state, символы: медиана %d, p90 %d, max %d" % (np.median(lens), np.percentile(lens, 90), max(lens)))
