// analyze_profile.mjs — разбор ORT-профиля (chrome trace JSON) по категориям
// и операторам. Использование: node tools/analyze_profile.mjs logs/ort-profile_*.json
import fs from "node:fs";

const file = process.argv[2];
if (!file) {
  console.error("укажите файл профиля");
  process.exit(1);
}
const events = JSON.parse(fs.readFileSync(file, "utf8"));

// Категории: Session/Kernel/Node/... — считаем число и суммарный dur.
const byCat = new Map();
const nodeByProvider = new Map();
const nodeByName = new Map();
const kernelByName = new Map();

for (const e of events) {
  if (typeof e.dur !== "number") continue;
  byCat.set(e.cat, (byCat.get(e.cat) || 0) + e.dur);
  if (e.cat === "Node") {
    const prov = e.args?.provider || "?";
    nodeByProvider.set(prov, (nodeByProvider.get(prov) || 0) + e.dur);
    const name = e.args?.op_name || e.name || "?";
    nodeByName.set(name, (nodeByName.get(name) || 0) + e.dur);
  }
  if (e.cat === "Kernel") {
    const name = e.name || "?";
    kernelByName.set(name, (kernelByName.get(name) || 0) + e.dur);
  }
}

const fmt = (v) => (v / 1000).toFixed(1) + " мс";
const top = (m, n) =>
  [...m.entries()].sort((a, b) => b[1] - a[1]).slice(0, n);

console.log("=== по категориям ===");
for (const [k, v] of top(byCat, 10)) console.log(`${k}: ${fmt(v)}`);

console.log("\n=== Node execute по провайдерам ===");
for (const [k, v] of top(nodeByProvider, 10)) console.log(`${k}: ${fmt(v)}`);

console.log("\n=== топ операторов (Node) ===");
for (const [k, v] of top(nodeByName, 15)) console.log(`${k}: ${fmt(v)}`);

console.log("\n=== топ CUDA-ядер (Kernel) ===");
for (const [k, v] of top(kernelByName, 15)) console.log(`${k}: ${fmt(v)}`);
