"""Export a Laya checkpoint to ONNX with a truly dynamic sequence length,
then check parity against PyTorch at several lengths and batch widths."""
import sys, time, json, os
import numpy as np, torch
from laya.agent import Agent
from laya.common import build_sequence, collate_items, QTYPES

ckpt, out = sys.argv[1], sys.argv[2]
agent = Agent(ckpt, compile=False, device="cpu")
model = agent.model.float().eval()
tok = agent.tok

def batch_for(texts, q):
    items = []
    for s in texts:
        seq, mk = build_sequence(tok, s, q, agent.cfg["max_len"], agent.cfg["head_max_len"])
        items.append({"ids": seq, "markers": mk, "qtype": QTYPES[q["t"]]})
    b = collate_items([items], tok.pad_token_id)
    return (b["input_ids"], b["attention_mask"], b["marker_pos"], b["marker_mask"], b["qtype"])

Q = {"t": "choice", "ins": "What is the relationship between `premise` and `hypothesis`?",
     "crit": {"entailment": "the premise implies the hypothesis is true",
              "neutral": "the premise neither implies nor contradicts the hypothesis",
              "contradiction": "the premise implies the hypothesis is false"}}
ex = batch_for([{"premise": "a b c", "hypothesis": "d e f"}, {"premise": "x " * 40, "hypothesis": "y"}], Q)

B, L, K = torch.export.Dim("batch", max=64), torch.export.Dim("seq", min=8, max=8192), torch.export.Dim("markers", max=255)
t0 = time.time()
with torch.no_grad():
    prog = torch.onnx.export(
        model, ex, dynamo=True, opset_version=18,
        input_names=["input_ids", "attention_mask", "marker_pos", "marker_mask", "qtype"],
        output_names=["logits", "act_logits"],
        dynamic_shapes=({0: B, 1: L}, {0: B, 1: L}, {0: B, 1: K}, {0: B, 1: K}, {0: B}),
    )
prog.optimize()
prog.save(out, external_data=True)
print("exported in %.0fs" % (time.time() - t0))

import onnxruntime as ort
sess = ort.InferenceSession(out, providers=["CPUExecutionProvider"])
worst = 0.0
for texts in ([{"premise": "the cache uses 7 retries", "hypothesis": "the cache uses 19 retries"}],
              [{"premise": "word " * 150, "hypothesis": "other " * 3}, {"premise": "short", "hypothesis": "x"}],
              [{"premise": "Engram uses Rust " * 30, "hypothesis": "TepinDB " * 60}]):
    ins = batch_for(texts, Q)
    with torch.no_grad():
        ref = model(*ins)[0].numpy()
    got = sess.run(["logits"], {n: t.numpy() for n, t in zip(
        ["input_ids", "attention_mask", "marker_pos", "marker_mask", "qtype"], ins)})[0]
    m = ins[3].numpy()
    d = np.abs(ref - got)[m].max()
    worst = max(worst, d)
    print("seq", ins[0].shape, "max |dlogit|", d)
print("WORST", worst)
