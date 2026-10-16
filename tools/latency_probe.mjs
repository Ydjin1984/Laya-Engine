// latency_probe.mjs — разложение latency /decide: число вопросов, длина state,
// параллельные запросы. Только локальный сервер, без внешних вызовов.
// Использование: node tools/latency_probe.mjs [port]
import http from "node:http";

const PORT = Number(process.argv[2] || 8095);

const Q = {
  a: { type: "noul", instructions: "Уничтожит ли это действие несохранённые данные?" },
  b: { type: "choice", instructions: "Категория риска?", criteria: { low: "безопасно", medium: "восстановимо", high: "потеря данных" } },
  c: { type: "score", instructions: "Сложность?", criteria: ["просто", "средне", "сложно"] },
};

function post(body) {
  return new Promise((resolve, reject) => {
    const payload = JSON.stringify(body);
    const req = http.request(
      { hostname: "127.0.0.1", port: PORT, path: "/decide", method: "POST",
        headers: { "content-type": "application/json", "content-length": Buffer.byteLength(payload) } },
      (res) => {
        const chunks = [];
        res.on("data", (c) => chunks.push(c));
        res.on("end", () => {
          try { resolve(JSON.parse(Buffer.concat(chunks).toString("utf8"))); }
          catch (e) { reject(e); }
        });
      },
    );
    req.on("error", reject);
    req.end(payload);
  });
}

function makeState(chars) {
  const base = "Инструмент: Bash\nКоманда: ";
  const tail = "\nПуть: build (внутри проекта)";
  return base + "x".repeat(Math.max(1, chars - base.length - tail.length)) + tail;
}

async function run(label, state, questions, n) {
  const lat = [];
  for (let i = 0; i < n; i++) {
    const t0 = Date.now();
    const r = await post({ state, questions });
    lat.push({ ms: Date.now() - t0, el: r.elapsed_ms, st: r.usage?.state_tokens ?? r.state_tokens, it: r.usage?.input_tokens });
  }
  lat.sort((a, b) => a.ms - b.ms);
  const med = lat[Math.floor(lat.length / 2)];
  const it = med.it ?? "-";
  console.log(`${label}: p50=${med.ms} мс (сервер ${med.el?.toFixed?.(1) ?? med.el} мс) state_tok=${med.st} input_tok=${it}`);
}

// разогрев
await post({ state: makeState(100), questions: { a: Q.a } });

console.log("--- масштабирование по числу вопросов (state 88 симв.) ---");
await run("n=1", makeState(88), { a: Q.a }, 5);
await run("n=2", makeState(88), { a: Q.a, b: Q.b }, 5);
await run("n=3", makeState(88), { a: Q.a, b: Q.b, c: Q.c }, 5);

console.log("--- масштабирование по длине state (3 вопроса) ---");
for (const chars of [88, 300, 681, 1100, 1400]) {
  await run(`state=${chars}`, makeState(chars), { a: Q.a, b: Q.b, c: Q.c }, 5);
}

console.log("--- параллельно: 3 хука одновременно (state 681, 3 вопроса) ---");
{
  const t0 = Date.now();
  const results = await Promise.all([1, 2, 3].map(() => post({ state: makeState(681), questions: { a: Q.a, b: Q.b, c: Q.c } })));
  const wall = Date.now() - t0;
  console.log(`wall=${wall} мс; серверные: ${results.map((r) => r.elapsed_ms?.toFixed?.(0)).join(", ")} мс`);
}
console.log("--- параллельно: 4 запроса по 1 вопросу (state 1400) ---");
{
  const t0 = Date.now();
  const results = await Promise.all([1, 2, 3, 4].map(() => post({ state: makeState(1400), questions: { a: Q.a } })));
  const wall = Date.now() - t0;
  console.log(`wall=${wall} мс; серверные: ${results.map((r) => r.elapsed_ms?.toFixed?.(0)).join(", ")} мс`);
}
