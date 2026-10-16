#!/usr/bin/env node
// ===========================================================================
// node_client.mjs -- minimal Laya Engine client (Node 18+, no dependencies).
//
// Run:  node examples/node_client.mjs [http://127.0.0.1:8095]
// ===========================================================================
const BASE = process.argv[2] ?? process.env.LAYA_BASE ?? 'http://127.0.0.1:8095';

const STATE = 'удали файл D:\\tmp\\old.txt и покажи список';

// Wording matters on a 322M model: `noul` answers take a full, concrete
// statement far better than a short abstract query about "the request".
// See docs/API.md ("Формулировки вопросов").
const QUESTIONS = {
  route: {
    type: 'choice',
    instructions: 'Какой набор инструментов нужен?',
    criteria: {
      dialog: 'просто ответить словами',
      files_write: 'изменить или удалить файл на диске',
      console: 'выполнить команду в терминале',
    },
  },
  risk: {
    type: 'score',
    instructions: 'Оцени риск действия по шкале.',
    criteria: ['низкий', 'средний', 'высокий'],
  },
  data_loss: {
    type: 'noul',
    instructions:
      'Уничтожит ли это действие несохранённые или невосстановимые данные ' +
      'пользователя (сброс изменений, удаление файлов, перезапись без копии)?',
  },
};

async function get(path) {
  const r = await fetch(BASE + path);
  if (!r.ok) throw new Error(`GET ${path} -> ${r.status}`);
  return r.json();
}

async function post(path, body) {
  const r = await fetch(BASE + path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!r.ok) throw new Error(`POST ${path} -> ${r.status}: ${await r.text()}`);
  return r.json();
}

async function waitReady(seconds = 180) {
  const deadline = Date.now() + seconds * 1000;
  while (Date.now() < deadline) {
    try { await get('/ready'); return; } catch { await new Promise((r) => setTimeout(r, 1000)); }
  }
  throw new Error('engine is not ready -- start it first (scripts/laya.sh start / start-laya.cmd)');
}

const main = async () => {
  await waitReady();
  const info = await get('/info');
  const cp = info.checkpoint ?? {};
  const cpId = `${cp.file ?? '?'}-${(cp.sha256 ?? '').slice(0, 12)}`;
  console.log(`engine: device=${info.device} checkpoint=${cpId}`);

  // 1) budget without inference
  const count = await post('/count', { state: STATE, questions: QUESTIONS });
  console.log(`budget: state_tokens=${count.state_tokens} fits=${count.fits}`);

  // 2) one decide call: three question types, one forward pass
  const res = await post('/decide', { state: STATE, questions: QUESTIONS, max_len: 1024 });
  console.log(`checkpoint=${res.checkpoint}  elapsed=${res.elapsed_ms.toFixed(1)} ms  input_tokens=${res.usage.input_tokens}`);

  let usable = true;
  if (res.budget.state_truncated) {
    usable = false;
    console.log('WARNING: state truncated -- the tail was not seen; do not trust the answer blindly.');
  }
  for (const qid of res.order) {
    const a = res.answers[qid];
    if (a.type === 'choice') {
      console.log(`${qid}: choice=${a.choice}  answer_confidence=${a.answer_confidence.toFixed(3)}`);
    } else if (a.type === 'score') {
      console.log(`${qid}: score=${a.score.toFixed(2)}  answer_confidence=${a.answer_confidence.toFixed(3)}`);
    } else {
      console.log(`${qid}: noul=${a.noul.toFixed(3)}  answer_confidence=${a.answer_confidence.toFixed(3)}`);
    }
    if (a.collapsed) { usable = false; console.log(`  WARNING: options collapsed (${qid}) -- unreliable.`); }
  }
  console.log('result:', usable ? 'usable' : 'DEGRADED (see warnings)');
};

main().catch((e) => { console.error(e.message); process.exit(1); });
