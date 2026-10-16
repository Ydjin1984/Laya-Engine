// laya-safety-check.mjs — измеряет точность Лая на бинарных вопросах о змейке:
// 1) «приведёт ли ход к смерти» (факт известен точно),
// 2) «станет ли расстояние меньше» (числовое сравнение).
// Нужно понять, где модель надёжна, а где отвечает наугад.

const PORTS = process.argv[2] ? process.argv[2].split(',').map(Number) : [8101, 8102, 8103];
const N = Number(process.argv[3] || 40);
const COLS = 12, ROWS = 12;
const DIRS = [[0, -1], [1, 0], [0, 1], [-1, 0]];
const DIR_NAMES = ['вверх', 'вправо', 'вниз', 'влево'];
const NAMES = ['прямо', 'влево', 'вправо'];

function rng(seed) {
  let a = seed >>> 0;
  return () => {
    a |= 0; a = (a + 0x6D2B79F5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function makeCase(r) {
  // случайная змейка длины 3–12 в случайной позиции
  const len = 3 + Math.floor(r() * 10);
  const snake = [];
  let x = 2 + Math.floor(r() * (COLS - 4)), y = 2 + Math.floor(r() * (ROWS - 4));
  let d = Math.floor(r() * 4);
  for (let i = 0; i < len; i++) {
    snake.push({ x, y });
    const nx = x - DIRS[d][0], ny = y - DIRS[d][1];
    if (nx < 0 || ny < 0 || nx >= COLS || ny >= ROWS) break;
    x = nx; y = ny;
  }
  const grid = new Uint8Array(COLS * ROWS);
  for (const s of snake) grid[s.y * COLS + s.x] = 1;
  const food = { x: Math.floor(r() * COLS), y: Math.floor(r() * ROWS) };
  if (grid[food.y * COLS + food.x]) { food.x = (food.x + 3) % COLS; food.y = (food.y + 2) % ROWS; }
  const dir = d;

  const blocked = (cx, cy) => {
    if (cx < 0 || cy < 0 || cx >= COLS || cy >= ROWS) return true;
    if (!grid[cy * COLS + cx]) return false;
    const t = snake[snake.length - 1];
    if (!(cx === food.x && cy === food.y) && t.x === cx && t.y === cy) return false;
    return true;
  };

  const h = snake[0];
  const cur = Math.abs(h.x - food.x) + Math.abs(h.y - food.y);
  const moves = [dir, (dir + 3) % 4, (dir + 1) % 4].map((nd, a) => {
    const nx = h.x + DIRS[nd][0], ny = h.y + DIRS[nd][1];
    return {
      slot: a, next: { x: nx, y: ny }, deadly: blocked(nx, ny),
      dist: Math.abs(nx - food.x) + Math.abs(ny - food.y),
    };
  });
  return { h, food, cur, dir, len, moves };
}

async function ask(port, state, questions) {
  const r = await fetch(`http://127.0.0.1:${port}/decide`, {
    method: 'POST', headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ state, questions }),
  });
  if (!r.ok) throw new Error('HTTP ' + r.status);
  return (await r.json()).answers;
}

function mortalText(c, m) {
  const d = DIR_NAMES[c.dir];
  return `Игра «Змейка», поле ${COLS}x${ROWS}. Голова змейки в клетке (${c.h.x},${c.h.y}), движется ${d}, длина ${c.len}. ` +
    `Рассматриваемый ход «${NAMES[m.slot]}»: змейка пойдёт в клетку (${m.next.x},${m.next.y}).`;
}

function closerText(c, m) {
  return `Игра «Змейка», поле ${COLS}x${ROWS}. Голова змейки в клетке (${c.h.x},${c.h.y}), движется ${DIR_NAMES[c.dir]}. ` +
    `Яблоко в клетке (${c.food.x},${c.food.y}). Сейчас расстояние от змейки до яблока равно ${c.cur}. ` +
    `Рассматриваемый ход «${NAMES[m.slot]}»: змейка пойдёт в свободную клетку (${m.next.x},${m.next.y}), ` +
    `после этого расстояние до яблока станет равно ${m.dist}.`;
}

const r = rng(777);
let mortalOk = 0, mortalTotal = 0;
let closerOk = 0, closerTotal = 0;
let portIdx = 0;
let deadlyCount = 0, safeCount = 0, closerYes = 0, closerNo = 0;

for (let i = 0; i < N; i++) {
  const c = makeCase(r);
  for (const m of c.moves) {
    const port = PORTS[portIdx++ % PORTS.length];
    // 1) смертельность
    try {
      const a = await ask(port, mortalText(c, m), {
        mortal: { type: 'noul', instructions: 'Приведёт ли этот ход к немедленной смерти змейки? Смерть — это стена или собственное тело змейки в указанной клетке.' },
      });
      const p = a.mortal?.noul ?? 0.5;
      const saidDeadly = p > 0.5;
      mortalTotal++;
      if (m.deadly) deadlyCount++; else safeCount++;
      if (saidDeadly === m.deadly) mortalOk++;
    } catch { mortalTotal++; }

    // 2) приближение (числовое сравнение)
    try {
      const a = await ask(port, closerText(c, m), {
        closer: { type: 'noul', instructions: `Стало ли расстояние до яблока меньше? Новое расстояние ${m.dist}, прежнее ${c.cur}.` },
      });
      const p = a.closer?.noul ?? 0.5;
      const said = p > 0.5;
      const truth = m.dist < c.cur;
      if (said) closerYes++; else closerNo++;
      closerTotal++;
      if (said === truth) closerOk++;
    } catch { closerTotal++; }
  }
  process.stdout.write(`\rпрогресс: ${i + 1}/${N}`);
}

console.log('\n');
console.log(`вопрос «этот ход смертелен?»:      точность ${((mortalOk / mortalTotal) * 100).toFixed(1)}%  (${mortalOk}/${mortalTotal}); смертельных ходов в выборке: ${deadlyCount}, безопасных: ${safeCount}`);
console.log(`вопрос «стало ближе к яблоку?»:    точность ${((closerOk / closerTotal) * 100).toFixed(1)}%  (${closerOk}/${closerTotal}); ответов «да»: ${closerYes}, «нет»: ${closerNo}`);
console.log('\nдля справки: случайное угадывание в бинарном вопросе — 50%');
