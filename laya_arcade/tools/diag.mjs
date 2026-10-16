// diag.mjs — диагностика обучения: длительность эпизодов, распределение фитнесов,
// реальные причины смертей. Помогает понять, где ломается градиент отбора.
import { Population } from '../src/brain.js';
import { SnakeEngine, MinesEngine, TetrisEngine } from '../src/games.js';

const specs = [
  ['snake', () => new SnakeEngine(), 20260928],
  ['mines', () => new MinesEngine(), 777123],
  ['tetris', () => new TetrisEngine(), 424242],
];
const seconds = Number(process.argv[2] || 20);
const perPopMs = Number(process.argv[3] || 4);

const pops = specs.map(([id, make, seed]) => {
  const eng = make();
  const pop = new Population(eng, { seed });
  pop.epStats = { n: 0, sum: 0, min: Infinity, max: 0 };
  pop.onEpisode = (m) => {
    const s = pop.epStats;
    s.n++; s.sum += m.steps; s.min = Math.min(s.min, m.steps); s.max = Math.max(s.max, m.steps);
  };
  return { id, eng, pop };
});

const t0 = performance.now();
while (performance.now() - t0 < seconds * 1000) {
  for (const s of pops) {
    const d = performance.now() + perPopMs;
    while (performance.now() < d) s.pop.tick();
  }
}

for (const s of pops) {
  const p = s.pop;
  const es = p.epStats;
  const avgSteps = es.n ? es.sum / es.n : 0;
  const sorted = p.members.slice().sort((a, b) => b.finalFitness - a.finalFitness);
  const fmtOne = (m) => `${m.finalFitness.toFixed(1)}/${m.score}`;
  console.log(`\n=== ${s.id} ===`);
  console.log(`поколений ${p.generation} за ${seconds} с (${(p.generation / seconds).toFixed(1)}/с), эпизодов ${p.episodes}`);
  console.log(`эпизод: средняя длина ${avgSteps.toFixed(1)} шагов, мин ${es.min} макс ${es.max}`);
  console.log(`рекорд ${p.bestScore}, средний счёт ${p.avgScore.toFixed(2)}, мутация ${p.mutationRate.toFixed(3)} ± ${p.mutationAmp.toFixed(3)}`);
  console.log(`фитнес/счёт (5 лучших): ${sorted.slice(0, 5).map(fmtOne).join('  ')}`);
  console.log(`фитнес/счёт (3 худших): ${sorted.slice(-3).map(fmtOne).join('  ')}`);
  const hist = p.history;
  console.log(`история рекордов: ${hist.slice(0, 3).map((h) => h.best).join(' → ')} … ${hist.slice(-4).map((h) => h.best).join(' → ')}`);
}
