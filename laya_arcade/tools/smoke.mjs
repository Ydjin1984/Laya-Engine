// smoke.mjs — проверка логики обучения без браузера:
// популяции реально сменяют поколения, рекорды растут, ошибок нет.
import { Population } from '../src/brain.js';
import { SnakeEngine, MinesEngine, TetrisEngine } from '../src/games.js';

const specs = [
  ['snake', () => new SnakeEngine(), 20260928],
  ['mines', () => new MinesEngine(), 777123],
  ['tetris', () => new TetrisEngine(), 424242],
];

const pops = specs.map(([id, make, seed]) => {
  const eng = make();
  const pop = new Population(eng, { seed });
  return { id, eng, pop };
});

const seconds = Number(process.argv[2] || 12);
const t0 = performance.now();
const deadline = t0 + seconds * 1000;
let ticks = 0;

while (performance.now() < deadline) {
  for (const s of pops) s.pop.tick();
  ticks++;
}

const elapsed = (performance.now() - t0) / 1000;
console.log(`тиков: ${ticks} за ${elapsed.toFixed(1)} с → ${Math.round(ticks / elapsed)} тик/с (по всем 3 популяциям сразу)`);
console.log('');

for (const s of pops) {
  const p = s.pop;
  const first = p.history[0];
  const last = p.history[p.history.length - 1];
  console.log(`[${s.id}] поколение ${p.generation}, эпизодов ${p.episodes}, рекорд ${p.bestScore}, средний счёт ${p.avgScore.toFixed(2)}`);
  console.log(`      первое поколение: рекорд ${first ? first.best : '—'} → последнее: ${last ? last.best : '—'} (средний фитнес ${last ? last.avgFit : '—'})`);
  console.log(`      мутация ${p.mutationRate.toFixed(3)} ± ${p.mutationAmp.toFixed(3)}, история ${p.history.length} точек`);
}

// Проверка, что обучение реально идёт: смена поколений обязательна
const totalGens = pops.reduce((a, s) => a + s.pop.generation, 0);
const totalEpisodes = pops.reduce((a, s) => a + s.pop.episodes, 0);
console.log('');
console.log(`ИТОГ: поколений суммарно ${totalGens}, эпизодов ${totalEpisodes}`);

let ok = true;
for (const s of pops) {
  if (s.pop.generation < 3) { console.log(`!! [${s.id}] за ${seconds} с сменилось меньше 3 поколений — обучение слишком медленное`); ok = false; }
  if (!s.pop.history.length) { console.log(`!! [${s.id}] нет истории поколений`); ok = false; }
}
if (pops[0].pop.bestScore < 3) { console.log('!! змейка: рекорд меньше 3 — обучение не прогрессирует'); ok = false; }
console.log(ok ? 'SMOKE: OK' : 'SMOKE: ПРОБЛЕМЫ');
process.exit(ok ? 0 : 1);
