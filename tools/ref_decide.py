"""Эталонные ответы Laya (Python) для сверки с Go-движком и замера точности.

Повторяет laya/common.py (build_sequence, collate_items, render_options) и
laya/onnx_agent.py (_decode_answers) без torch — только tokenizers + numpy +
onnxruntime. Это ИНСТРУМЕНТ РАЗРАБОТКИ: сам движок работает на Go и Python в
рантайме не требует.

Запуск: python tools/ref_decide.py [cases.json]
Выход: tools/ref_answers.json  (ответы + латентность + точность по route)
"""
import glob
import json
import math
import os
import site
import sys
import time

import numpy as np
import onnxruntime as ort
from tokenizers import Tokenizer


# Корень репозитория: пути считаются от расположения скрипта, а не от машины.
_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

def setup_gpu(device_id: int) -> list:
    """Готовит DLL CUDA 12/cuDNN 9 из пакетов nvidia-* и включает CUDA-провайдер.

    Эталон снимается ТОЛЬКО на видеокарте: CPU-прогон даёт другие числа и как
    эталон не годится (на CPU fp16-веса приводятся к fp32 через касты).
    """
    dirs = []
    for sp in set(site.getsitepackages() + [site.getusersitepackages()]):
        for d in glob.glob(os.path.join(sp, "nvidia", "*", "bin")):
            dirs.append(d)
    for d in dirs:
        try:
            os.add_dll_directory(d)
        except OSError:
            pass
        os.environ["PATH"] = d + os.pathsep + os.environ["PATH"]
    return [("CUDAExecutionProvider", {"device_id": device_id}), "CPUExecutionProvider"]

QTYPES = {"choice": 0, "score": 1, "noul": 2}
QTYPE_NAMES = {v: k for k, v in QTYPES.items()}
MASK = "<mask>"
# Путь к модели настраивается: int8-экспорт от techtheist оказался испорчен
# (квантование убило голову решений), рабочий вариант — fp16.
MODEL = os.environ.get("LAYA_MODEL", os.path.join(_ROOT, "models", "multilingual-fp16.onnx"))
TOKENIZER = os.path.join(_ROOT, "models", "tokenizer.json")
CASES = os.path.join(_ROOT, "tools", "cases.json")
OUT = os.environ.get("LAYA_REF_OUT", os.path.join(_ROOT, "tools", "ref_answers.json"))


def render_criterion(v):
    if isinstance(v, str):
        return v
    return json.dumps(v, ensure_ascii=False, separators=(", ", ": "), default=str)


def render_options(q):
    t, crit = q["type"], q.get("criteria")
    if t == "choice":
        return [str(k) if v is None or v == "" else "%s: %s" % (k, render_criterion(v))
                for k, v in crit.items()]
    if t == "score":
        return ["level %d: %s" % (i, render_criterion(c)) for i, c in enumerate(crit)]
    crit = crit or {}
    labels = q.get("labels") or {"false": "false", "true": "true"}
    false_crit, true_crit = crit.get("false"), crit.get("true")
    return [
        labels["false"] + ": " + (render_criterion(false_crit) if false_crit not in (None, "") else "no, the statement does not hold"),
        labels["true"] + ": " + (render_criterion(true_crit) if true_crit not in (None, "") else "yes, the statement holds"),
    ]


def build_sequence(tok, state, q, max_len, head_max_len, state_ids=None):
    opts = render_options(q)
    ins = str(q["instructions"]).replace(MASK, " ")
    head_ids = tok.encode("%s question: %s" % (q["type"], ins), add_special_tokens=False).ids
    opt_ids = []
    for o in opts:
        body = " " + o.replace(MASK, " ")
        toks = tok.encode(body, add_special_tokens=False).ids[:48]
        opt_ids.append([tok.token_to_id(MASK)] + toks)
    opt_budget = head_max_len - sum(len(o) for o in opt_ids)
    per_option = None
    if opt_budget < 16:
        per = max(4, (head_max_len - 16) // max(1, len(opt_ids)))
        per_option = per
        opt_ids = [o[:per] for o in opt_ids]
        opt_budget = head_max_len - sum(len(o) for o in opt_ids)
    head_ids = head_ids[: max(8, opt_budget)]
    ids = [tok.token_to_id("<eos>")] + head_ids + [tok.token_to_id("<eos>")]
    markers = []
    for o in opt_ids:
        markers.append(len(ids))
        ids.extend(o)
    ids.append(tok.token_to_id("<eos>"))
    room = max(0, max_len - len(ids) - 1)
    if state_ids is None:
        state_ids = tok.encode(str(state).replace(MASK, " "), add_special_tokens=False).ids
    st = state_ids[:room]
    ids = ids + st + [tok.token_to_id("<eos>")]
    ids = ids[:max_len]
    markers = [m for m in markers if m < max_len]
    stats = {"options": len(opt_ids),
             "options_distinct": len({tuple(o) for o in opt_ids}),
             "tokens_per_option": per_option}
    return ids, markers, stats


def temp_bucket(qt, k):
    size = "2" if k <= 2 else "3-5" if k <= 5 else "6-10" if k <= 10 else "11+"
    return "%s:%s" % (QTYPE_NAMES[qt], size)


def entropy_conf(p, k):
    if k < 2:
        return 1.0
    ent = -(p * np.log(np.clip(p, 1e-12, 1.0))).sum()
    return float(np.clip(1.0 - ent / math.log(k), 0.0, 1.0))


def main():
    cases_path = sys.argv[1] if len(sys.argv) > 1 else CASES
    with open(cases_path, encoding="utf-8") as f:
        data = json.load(f)
    questions = data["questions"]
    cases = data["cases"]
    max_len = data.get("max_len", 1024)
    head_max_len = data.get("head_max_len", 256)

    tok = Tokenizer.from_file(TOKENIZER)
    so = ort.SessionOptions()
    device_id = int(os.environ.get("LAYA_DEVICE_ID", "1"))  # 3060 — карта #1
    providers = setup_gpu(device_id)
    sess = ort.InferenceSession(MODEL, sess_options=so, providers=providers)
    print("провайдеры сессии:", sess.get_providers(), "| карта #%d" % device_id)
    if "CUDAExecutionProvider" not in sess.get_providers():
        print("ОШИБКА: CUDA не поднялась — эталон на CPU не принимается")
        return 2
    qids = list(questions.keys())

    results = []
    lat = []
    for ci, case in enumerate(cases):
        state = case["state"]
        state_ids = tok.encode(str(state).replace(MASK, " "), add_special_tokens=False).ids
        items = []
        for qid in qids:
            q = questions[qid]
            seq, markers, stats = build_sequence(tok, state, q, max_len, head_max_len, state_ids)
            items.append({"ids": seq, "markers": markers, "qtype": QTYPES[q["type"]], "stats": stats})

        n, L = len(items), max(len(it["ids"]) for it in items)
        kmax = max(len(it["markers"]) for it in items)
        ids = np.full((n, L), tok.token_to_id("<pad>"), dtype=np.int64)
        att = np.zeros((n, L), dtype=np.int64)
        mpos = np.zeros((n, kmax), dtype=np.int64)
        mmask = np.zeros((n, kmax), dtype=bool)
        qtype = np.array([it["qtype"] for it in items], dtype=np.int64)
        for i, it in enumerate(items):
            ids[i, : len(it["ids"])] = it["ids"]
            att[i, : len(it["ids"])] = 1
            k = len(it["markers"])
            mpos[i, :k] = it["markers"]
            mmask[i, :k] = True

        t0 = time.perf_counter()
        outs = sess.run(["logits", "act_logits"], {
            "input_ids": ids, "attention_mask": att,
            "marker_pos": mpos, "marker_mask": mmask, "qtype": qtype,
        })
        dt = (time.perf_counter() - t0) * 1000.0
        lat.append(dt)
        logits, act_logits = outs[0], outs[1]
        act_exp = np.exp(act_logits - np.max(act_logits, axis=-1, keepdims=True))
        act = act_exp / act_exp.sum(axis=-1, keepdims=True)

        answers = {}
        for r, qid in enumerate(qids):
            q = questions[qid]
            k = len(items[r]["markers"])
            qt = QTYPES[q["type"]]
            z = logits[r, :k] / 1.0  # temperature = 1.0 в этом чекпоинте
            p = np.exp(z - z.max())
            p = p / p.sum()
            a = {
                "confidence": round(entropy_conf(p, k), 4),
                "answer_confidence": round(float(np.clip(p.max(), 0, 1)), 4),
                "act_probability": round(float(act[r, 0]), 4),
                "probs": [round(float(v), 4) for v in p],
            }
            if q["type"] == "choice":
                keys = list(q["criteria"].keys())
                a["choice"] = keys[int(p.argmax())]
            elif q["type"] == "score":
                a["score"] = round(float((np.arange(k) * p).sum()), 4)
            else:
                a["noul"] = round(float(p[1]), 4)
            answers[qid] = a

        results.append({"id": case["id"], "fact_top": case["fact_top"], "answers": answers})
        if (ci + 1) % 25 == 0:
            print("  обработано", ci + 1, "из", len(cases))

    lat = np.array(lat)
    correct = sum(1 for r in results if r["answers"]["route"].get("choice") == r["fact_top"])
    # Точность по мажоритарному классу — нижняя граница «полезности» предсказания.
    from collections import Counter
    majority = Counter(c["fact_top"] for c in cases).most_common(1)[0]
    out = {
        "model": MODEL,
        "cases": len(cases),
        "latency_ms": {"p50": float(np.percentile(lat, 50)), "p95": float(np.percentile(lat, 95)),
                       "mean": float(lat.mean()), "max": float(lat.max())},
        "route_accuracy": correct / max(1, len(results)),
        "majority_baseline": majority[1] / max(1, len(cases)),
        "majority_class": majority[0],
        "results": results,
    }
    with open(OUT, "w", encoding="utf-8") as f:
        json.dump(out, f, ensure_ascii=False, indent=1)
    print("латентность, мс: p50=%.1f p95=%.1f mean=%.1f" % (out["latency_ms"]["p50"], out["latency_ms"]["p95"], out["latency_ms"]["mean"]))
    print("route accuracy: %.3f   (мажоритарный baseline %.3f = класс %s)" % (
        out["route_accuracy"], out["majority_baseline"], out["majority_class"]))
    print("wrote", OUT)


if __name__ == "__main__":
    main()
