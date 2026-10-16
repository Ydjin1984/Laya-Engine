// laya-play-snake.mjs — играет партии в Змейку тем же механизмом, что и страница:
// правила отбирают три безопасных осмысленных хода, Лай выбирает между ними.
// Показывает конечный результат: сколько яблок съедено и как выросла змейка.
import { SnakeEngine } from '../src/games.js';
import { mulberry32 } from '../src/brain.js';

const PORTS = process.argv[2] ? process.argv[2].split(',').map(Number) : [8101, 8102, 8103];
const GAMES = Number(process.argv[3] || 5);
const MAX_STEPS = 400;

const eng = new SnakeEngine();
const rng = mulberry32(Date.now() & 0xffff);

async function closer(port, text) {
  const r = await fetch(`http://127.0.0.1:${port}/decide`, {
    method: 'POST', headers: { 'content-type': 'application/json' },
    body: JSON.stringify({
      state: text,
      questions: {
        closer: { type: 'noul', instructions: 'Уменьшит ли этот ход расстояние от змейки до яблока? То есть станет ли змейка ближе к яблоку после этого хода?' },
      },
    }),
  });
  if (!r.ok) throw new Error('HTTP ' + r.status);
  return (await r.json()).answers.closer?.noul ?? 0.5;
}

let portIdx = 0;
const report = [];

for (let g = 1; g <= GAMES; g++) {
  const st = eng.newState(rng);
  let steps = 0, asks = 0, forced = 0, chosen = {};

  while (steps < MAX_STEPS) {
    const sit = eng.layaSituation(st);
    let action;
    if (!sit || !sit.options.length) break;

    if (sit.forced) {
      action = sit.options[0].action;
      forced++;
    } else {
      const perOption = 2;
      const jobs = [];
      sit.options.forEach((opt, i) => {
        for (let k = 0; k < perOption; k++) {
          const port = PORTS[(portIdx + i + k) % PORTS.length];
          jobs.push(closer(port, opt.text).then((p) => ({ i, p })).catch(() => null));
        }
      });
      portIdx += perOption;
      const got = (await Promise.all(jobs)).filter(Boolean);
      if (!got.length) break;
      const sums = sit.options.map(() => ({ s: 0, n: 0 }));
      for (const x of got) { sums[x.i].s += x.p; sums[x.i].n++; }
      const scored = sit.options.map((o, i) => ({ i, o, score: sums[i].n ? sums[i].s / sums[i].n : 0, dist: o.dist }));
      scored.sort((a, b) => b.score - a.score || a.dist - b.dist);
      action = scored[0].o.action;
      asks += got.length;
      chosen[scored[0].o.why] = (chosen[scored[0].o.why] || 0) + 1;
    }

    const r = eng.step(st, action, rng);
    steps++;
    if (r.done) break;
  }

  report.push({ len: st.snake.length, eaten: st.eaten, steps, reason: st.deathReason || 'лимит шагов' });
  const whyList = Object.entries(chosen).sort((a, b) => b[1] - a[1]).slice(0, 3)
    .map(([k, v]) => `${k} — ${v} раз`).join('; ');
  console.log(`партия ${g}: длина ${st.snake.length}, яблок ${st.eaten}, шагов ${steps}, финал: ${st.deathReason || 'лимит шагов'}`);
  console.log(`   запросов к Лаям: ${asks}, ходов по правилам без вопроса: ${forced}`);
  console.log(`   частые решения Лая: ${whyList || '—'}`);
}

const eaten = report.reduce((a, x) => a + x.eaten, 0);
const len = report.reduce((a, x) => a + x.len, 0);
console.log(`\nИТОГО: партий ${GAMES}, яблок съедено ${eaten} (в среднем ${(eaten / GAMES).toFixed(1)}), средняя длина ${(len / GAMES).toFixed(1)}`);
console.log('для сравнения: жадная эталонная политика даёт ~20 яблок за партию');
