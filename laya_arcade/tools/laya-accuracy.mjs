// laya-accuracy.mjs — измеряет, насколько точно Лай оценивает ходы в Змейке,
// на РАЗНЫХ типах вопросов (choice / noul / score). Цель — найти форму вопроса,
// на которой модель действительно работает, а не отвечает наугад.
//
// Эталон: среди не-смертельных ходов берём тот, что уменьшает расстояние до яблока.

const PORTS = [8101, 8102, 8103];
const SITUATIONS = Number(process.argv[2] || 16);
const SEED = 4242;

const DIRS = [[0, -1], [1, 0], [0, 1], [-1, 0]];
const DIR_NAMES = ['вверх', 'вправо', 'вниз', 'влево'];
const COLS = 12, ROWS = 12, HUNGER = 60;

function rng(seed) {
  let a = seed >>> 0;
  return () => {
    a |= 0; a = (a + 0x6D2B79F5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function newState(r) {
  const cx = 6, cy = 6;
  const st = {
    snake: [{ x: cx, y: cy }, { x: cx - 1, y: cy }, { x: cx - 2, y: cy }],
    grid: new Uint8Array(COLS * ROWS), dir: 1, food: { x: 0, y: 0 }, eaten: 0, stepCount: 0, stepsSinceFood: 0,
    lastVisit: new Int32Array(COLS * ROWS).fill(-999), dead: false,
  };
  for (const s of st.snake) { st.grid[s.y * COLS + s.x] = 1; st.lastVisit[s.y * COLS + s.x] = 0; }
  placeFood(st, r);
  return st;
}

function placeFood(st, r) {
  const free = [];
  for (let i = 0; i < st.grid.length; i++) if (!st.grid[i]) free.push(i);
  const i = free[Math.floor(r() * free.length)];
  st.food = { x: i % COLS, y: Math.floor(i / COLS) };
}

function blocked(st, x, y) {
  if (x < 0 || y < 0 || x >= COLS || y >= ROWS) return true;
  if (!st.grid[y * COLS + x]) return false;
  const t = st.snake[st.snake.length - 1];
  const foodAhead = (x === st.food.x && y === st.food.y);
  if (!foodAhead && t.x === x && t.y === y) return false;
  return true;
}

function recentVisit(st, x, y, w = 25) {
  if (x < 0 || y < 0 || x >= COLS || y >= ROWS) return false;
  const t = st.lastVisit[y * COLS + x];
  return t >= 0 && st.stepCount - t < w;
}

function moves(st) {
  const h = st.snake[0];
  const left = (st.dir + 3) % 4, right = (st.dir + 1) % 4;
  return [st.dir, left, right].map((nd, a) => {
    const nx = h.x + DIRS[nd][0], ny = h.y + DIRS[nd][1];
    const deadly = blocked(st, nx, ny);
    return {
      slot: a, dir: nd, next: { x: nx, y: ny }, deadly,
      dist: Math.abs(nx - st.food.x) + Math.abs(ny - st.food.y),
      cur: Math.abs(h.x - st.food.x) + Math.abs(h.y - st.food.y),
      circling: !deadly && recentVisit(st, nx, ny),
      free: deadly ? 0 : [0, 1, 2, 3].filter((d) => !blocked(st, nx + DIRS[d][0], ny + DIRS[d][1])).length,
    };
  });
}

function optimal(mv) {
  const safe = mv.filter((m) => !m.deadly);
  if (!safe.length) return null;
  safe.sort((a, b) => a.dist - b.dist || Number(a.circling) - Number(b.circling));
  return safe[0].slot;
}

function step(st, slot, r) {
  const m = moves(st)[slot];
  st.dir = m.dir;
  st.stepCount++;
  if (m.deadly) { st.dead = true; return; }
  const h = m.next;
  st.snake.unshift({ x: h.x, y: h.y });
  st.grid[h.y * COLS + h.x] = 1;
  st.lastVisit[h.y * COLS + h.x] = st.stepCount;
  if (h.x === st.food.x && h.y === st.food.y) { st.eaten++; st.stepsSinceFood = 0; placeFood(st, r); }
  else {
    const tail = st.snake.pop();
    st.grid[tail.y * COLS + tail.x] = 0;
    st.stepsSinceFood++;
    if (st.stepsSinceFood > HUNGER) st.dead = true;
  }
}

function collectSituations(count, seed) {
  const r = rng(seed);
  const out = [];
  let guard = 0;
  while (out.length < count && guard++ < 200) {
    const st = newState(r);
    for (let n = 0; n < 120 && !st.dead; n++) {
      const mv = moves(st);
      const safe = mv.filter((m) => !m.deadly);
      if (!safe.length) break;
      if (safe.length > 1 && r() < 0.5) {
        out.push({
          dir: st.dir, head: { ...st.snake[0] }, food: { ...st.food }, len: st.snake.length,
          eaten: st.eaten, stepsSinceFood: st.stepsSinceFood,
          mv: mv.map((m) => ({ ...m })), optimal: optimal(mv),
        });
        if (out.length >= count) break;
      }
      const slot = (guard % 2 === 0) ? optimal(mv) : safe[Math.floor(r() * safe.length)].slot;
      if (slot === null) break;
      step(st, slot, r);
    }
  }
  return out;
}

async function ask(port, payload) {
  const r = await fetch(`http://127.0.0.1:${port}/decide`, {
    method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(payload),
  });
  if (!r.ok) throw new Error('HTTP ' + r.status);
  return r.json();
}

const NAMES = ['прямо', 'влево', 'вправо'];
const CRIT = ['straight', 'left', 'right'];

/** Строит описание одного хода для per-variant вопросов. */
function variantState(st, m) {
  const h = st.snake[0];
  return `Игра «Змейка», поле ${COLS}x${ROWS}. Голова змейки в клетке (${h.x},${h.y}), движется ${DIR_NAMES[st.dir]}. ` +
    `Яблоко в клетке (${st.food.x},${st.food.y}), расстояние до него сейчас ${m.cur}. ` +
    `Рассматриваемый ход «${NAMES[m.slot]}»: змейка пойдёт в клетку (${m.next.x},${m.next.y}), которая ${m.deadly ? 'ЗАНЯТА (стена или своё тело)' : 'свободна'}. ` +
    (m.deadly ? '' : `После хода расстояние до яблока станет ${m.dist}, свободных клеток рядом будет ${m.free} из 4.`);
}

/* ---------------- тестируемые подходы ---------------- */
const METHODS = {
  // 1) один choice-вопрос со всеми тремя ходами
  choice_multi: {
    kind: 'single',
    build(st, mv) {
      const cur = mv[0].cur;
      const crit = {}, names = {};
      mv.forEach((m) => {
        crit[CRIT[m.slot]] = m.deadly ? `ход ${NAMES[m.slot]}: смерть` : `ход ${NAMES[m.slot]}: расстояние до яблока станет ${m.dist}`;
      });
      return {
        state: `Змейка. Голова (${st.snake[0].x},${st.snake[0].y}). Яблоко (${st.food.x},${st.food.y}). Расстояние ${cur}.`,
        questions: { move: { type: 'choice', instructions: 'Какой ход уменьшает расстояние до яблока и не убивает змейку?', criteria: crit } },
      };
    },
    pick(json) {
      const a = json.answers.move;
      if (!a) return null;
      if (a.choice) return CRIT.indexOf(a.choice);
      if (a.probabilities) {
        let best = -1, bi = null;
        a.probabilities.forEach((p) => { if (p.value > best) { best = p.value; bi = CRIT.indexOf(p.label); } });
        return bi;
      }
      return null;
    },
  },

  // 2) на каждый ход два бинарных вопроса: смертелен? приближает?
  noul_per_variant: {
    kind: 'per_variant',
    build(st, m) {
      return {
        state: variantState(st, m),
        questions: {
          mortal: { type: 'noul', instructions: 'Приведёт ли этот ход к немедленной смерти змейки (стена или её собственное тело)?' },
          closer: { type: 'noul', instructions: 'Уменьшит ли этот ход расстояние от змейки до яблока?' },
        },
      };
    },
    evaluate(json) {
      const mortal = json.answers.mortal?.noul ?? 0.5;
      const closer = json.answers.closer?.noul ?? 0.5;
      if (mortal > 0.5) return -10 + (1 - mortal);
      return closer;
    },
  },

  // 3) на каждый ход оценка по шкале
  score_per_variant: {
    kind: 'per_variant',
    build(st, m) {
      return {
        state: variantState(st, m),
        questions: {
          quality: {
            type: 'score',
            instructions: 'Насколько хорош этот ход для змейки?',
            levels: [
              'змейка погибнет — это худший ход',
              'плохой ход: змейка уйдёт от яблока и может попасть в ловушку',
              'нейтральный ход',
              'хороший ход: змейка приблизится к яблоку',
              'лучший ход: змейка съест яблоко',
            ],
          },
        },
      };
    },
    evaluate(json) {
      const q = json.answers.quality;
      if (!q) return 0;
      const levels = q.levels || q.probabilities;
      if (levels && Array.isArray(levels)) {
        let sum = 0, w = 0;
        levels.forEach((p, i) => { const v = p.value ?? p.probability ?? 0; sum += v * i; w += v; });
        return w > 0 ? sum / w : 0;
      }
      if (typeof q.score === 'number') return q.score;
      return 0;
    },
  },

  // 4) бинарный вопрос только про приближение (без смертельного фильтра)
  closer_only: {
    kind: 'per_variant',
    build(st, m) {
      return {
        state: variantState(st, m),
        questions: {
          closer: { type: 'noul', instructions: 'Станет ли змейка ближе к яблоку после этого хода?' },
        },
      };
    },
    evaluate(json) {
      const m = json.answers.closer?.noul ?? 0.5;
      return m;
    },
  },
};

async function run() {
  const situations = collectSituations(SITUATIONS, SEED);
  console.log(`ситуаций: ${situations.length}; эталон — жадный безопасный ход\n`);

  const table = {};
  for (const name of Object.keys(METHODS)) table[name] = { ok: 0, total: 0, lethalChoice: 0, ms: 0 };

  for (let i = 0; i < situations.length; i++) {
    const sit = situations[i];
    // псевдо-состояние для построителей
    const st = {
      snake: Array.from({ length: sit.len }, () => ({ ...sit.head })),
      dir: sit.dir, food: sit.food, eaten: sit.eaten, stepsSinceFood: sit.stepsSinceFood,
      grid: new Uint8Array(COLS * ROWS), lastVisit: new Int32Array(COLS * ROWS).fill(-999), dead: false, stepCount: 0,
    };

    for (const [name, method] of Object.entries(METHODS)) {
      const t = table[name];
      t.total++;
      const port = PORTS[i % PORTS.length];
      const t0 = Date.now();
      try {
        let slot = null;
        if (method.kind === 'single') {
          const payload = method.build(st, sit.mv);
          const json = await ask(port, payload);
          slot = method.pick(json);
        } else {
          const results = [];
          for (const m of sit.mv) {
            const payload = method.build(st, m);
            const json = await ask(port, payload);
            results.push({ slot: m.slot, score: method.evaluate(json), deadly: m.deadly });
          }
          results.sort((a, b) => b.score - a.score);
          slot = results[0].slot;
          if (results[0].deadly) t.lethalChoice++;
        }
        t.ms += Date.now() - t0;
        if (slot === sit.optimal) t.ok++;
      } catch (e) {
        t.ms += Date.now() - t0;
      }
    }
    process.stdout.write(`\rпрогресс: ${i + 1}/${situations.length}`);
  }

  console.log('\n');
  console.log('метод                точность   выбрал смертельный   среднее время');
  for (const [name, r] of Object.entries(table)) {
    const acc = r.total ? ((r.ok / r.total) * 100).toFixed(1) : '—';
    const lc = r.total ? ((r.lethalChoice / r.total) * 100).toFixed(1) : '—';
    const ms = r.total ? (r.ms / r.total).toFixed(0) : '—';
    console.log(`${name.padEnd(20)} ${String(acc).padStart(6)}%   ${String(lc).padStart(17)}%   ${String(ms).padStart(9)} мс`);
  }
  console.log(`\n(случайное угадывание из трёх вариантов ≈ 33%, идеал — 100%)`);
}

run();
