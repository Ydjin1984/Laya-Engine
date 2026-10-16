// sanity.mjs — проверка, что среда Змейки «решаема»: эталонная жадная политика
// (идти к еде, избегая стен и себя) должна показывать высокий результат.
// Если эталон играет хорошо — среда исправна, а медленный рост объясняется обучением.
import { SnakeEngine } from '../src/games.js';
import { mulberry32 } from '../src/brain.js';

const eng = new SnakeEngine();
const rng = mulberry32(99);
const DIRS = [[0, -1], [1, 0], [0, 1], [-1, 0]];

function greedy(st) {
  const h = st.snake[0];
  const options = [st.dir, (st.dir + 3) % 4, (st.dir + 1) % 4];
  let best = null;
  for (const d of options) {
    const nx = h.x + DIRS[d][0], ny = h.y + DIRS[d][1];
    if (eng.blocked(st, nx, ny, true)) continue;
    const dist = Math.abs(nx - st.food.x) + Math.abs(ny - st.food.y);
    if (!best || dist < best.dist) best = { d, dist };
  }
  if (!best) return st.dir;      // все ходы смертельны — идём как есть
  // относительное действие
  if (best.d === st.dir) return 0;
  if (best.d === (st.dir + 3) % 4) return 1;
  return 2;
}

const runs = [];
for (let e = 0; e < 50; e++) {
  const st = eng.newState(rng);
  let steps = 0;
  while (steps < eng.maxSteps) {
    const a = greedy(st);
    const r = eng.step(st, a, rng);
    steps++;
    if (r.done) break;
  }
  runs.push({ len: eng.score(st), steps, food: st.eaten, fitscore: 0 });
}
const avg = (k) => (runs.reduce((a, r) => a + r[k], 0) / runs.length).toFixed(1);
console.log(`эталонная жадная политика, 50 эпизодов:`);
console.log(`  средняя длина змейки: ${avg('len')}, средняя съеденная еда: ${avg('food')}, средняя длительность: ${avg('steps')} шагов`);
console.log(`  лучший результат: ${Math.max(...runs.map((r) => r.len))}, худший: ${Math.min(...runs.map((r) => r.len))}`);
console.log(`  поле ${eng.cols}×${eng.rows}, лимит шагов ${eng.maxSteps}`);
