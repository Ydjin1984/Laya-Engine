// question_cost.mjs — сколько времени реально стоит каждый вопрос риск-гейта.
// Формулировки — копия riskQuestions() плагина (v1) + варианты урезания.
import http from "node:http";

function post(body) {
  return new Promise((resolve, reject) => {
    const payload = JSON.stringify(body);
    const req = http.request(
      { hostname: "127.0.0.1", port: 8095, path: "/decide", method: "POST",
        headers: { "content-type": "application/json", "content-length": Buffer.byteLength(payload) } },
      (res) => {
        const c = [];
        res.on("data", (d) => c.push(d));
        res.on("end", () => { try { resolve(JSON.parse(Buffer.concat(c))); } catch (e) { reject(e); } });
      },
    );
    req.on("error", reject);
    req.end(payload);
  });
}

// --- реальные вопросы плагина (v1) ---
const Q_DATA_LOSS = { type: "noul", instructions: "Уничтожит ли это действие несохранённые или невосстановимые данные пользователя (сброс изменений, удаление файлов, перезапись без копии)?" };
const Q_EXFIL = { type: "noul", instructions: "Может ли это действие отправить секрет (токен, пароль, ключ, содержимое .env) за пределы машины или записать его в открытый файл?" };
const Q_RISK = { type: "choice", instructions: "Выбери категорию риска действия, если выполнить его как есть.", criteria: {
  low: "Чтение или безвредная операция, легко отменяется",
  medium: "Меняет обычные файлы проекта, восстановление возможно (git, бэкап)",
  high: "Потеря данных, утечка секретов, необратимое или внешнее действие",
} };

const state = (chars) => {
  const base = "Инструмент: Bash\nКоманда: ";
  const tail = "\nПуть: build (внутри проекта)\nПравила: truncate-file(medium)";
  return base + "x".repeat(Math.max(1, chars - base.length - tail.length)) + tail;
};

async function bench(label, st, questions, n = 6) {
  const lat = [];
  let el = 0;
  for (let i = 0; i < n; i++) {
    const t0 = Date.now();
    const r = await post({ state: st, questions });
    lat.push(Date.now() - t0);
    el += r.elapsed_ms || 0;
  }
  lat.sort((a, b) => a - b);
  console.log(`${label}: p50=${lat[Math.floor(n / 2)]} мс (сервер ${(el / n).toFixed(0)} мс)`);
}

await post({ state: state(130), questions: { risk_class: Q_RISK } }); // разогрев

console.log("--- state 130 токенов (~414 симв.) ---");
await bench("все 3 вопроса (v1)      ", state(414), { data_loss: Q_DATA_LOSS, exfiltration: Q_EXFIL, risk_class: Q_RISK });
await bench("risk_class + data_loss  ", state(414), { data_loss: Q_DATA_LOSS, risk_class: Q_RISK });
await bench("только risk_class       ", state(414), { risk_class: Q_RISK });

console.log("--- state ~370 токенов (~1180 симв., p95) ---");
await bench("все 3 вопроса (v1)      ", state(1180), { data_loss: Q_DATA_LOSS, exfiltration: Q_EXFIL, risk_class: Q_RISK });
await bench("только risk_class       ", state(1180), { risk_class: Q_RISK });
