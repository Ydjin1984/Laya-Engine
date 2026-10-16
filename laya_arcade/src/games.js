// games.js — три игровые среды для обучающегося Лая.
// Каждая среда сама решает, КАК применить мозг (argmax по выходам либо оценка вариантов),
// но не подсказывает агенту стратегию: веса добываются эволюцией.

const DIRS = [[0, -1], [1, 0], [0, 1], [-1, 0]]; // вверх, вправо, вниз, влево
const DIR_NAMES = ['вверх', 'вправо', 'вниз', 'влево'];

/* ================================================================== */
/* ЗМЕЙКА                                                              */
/* ================================================================== */
export class SnakeEngine {
  constructor() {
    this.id = 'snake';
    this.title = 'Змейка';
    this.subtitle = 'цель: расти (есть яблоки), не врезаться и не кружить · мозг: 24 признака → 18 нейронов → 3 действия';
    this.sizes = [24, 18, 3];
    this.population = 48;
    this.mutationRate = 0.12;
    this.mutationAmp = 0.42;
    this.cols = 12;
    this.rows = 12;
    this.maxSteps = 400;
    this.hungerLimit = 60;        // шагов без яблока — партия заканчивается
    this.series = 3;              // эпизодов на особь в одном поколении
    this.scoreBonus = 0.5;        // в фитнесе длина змейки учитывается напрямую: рост — это цель
    this.viewport = { w: 288, h: 288 };
  }

  newState(rng) {
    const cx = Math.floor(this.cols / 2), cy = Math.floor(this.rows / 2);
    const st = {
      snake: [{ x: cx, y: cy }, { x: cx - 1, y: cy }, { x: cx - 2, y: cy }],
      grid: new Uint8Array(this.cols * this.rows),
      lastVisit: new Int32Array(this.cols * this.rows).fill(-999),
      dir: 1, food: { x: 0, y: 0 }, alive: true, eaten: 0, deathReason: '',
      stepCount: 0, stepsSinceFood: 0,
    };
    for (const s of st.snake) {
      st.grid[s.y * this.cols + s.x] = 1;
      st.lastVisit[s.y * this.cols + s.x] = 0;
    }
    this.placeFood(st, rng);
    return st;
  }

  /** Клетка, которую змейка занимала недавно: заход туда — это кружение. */
  recentVisit(st, x, y, window = 25) {
    if (x < 0 || y < 0 || x >= this.cols || y >= this.rows) return false;
    const t = st.lastVisit[y * this.cols + x];
    return t >= 0 && st.stepCount - t < window;
  }


  placeFood(st, rng) {
    const free = [];
    for (let i = 0; i < st.grid.length; i++) if (!st.grid[i]) free.push(i);
    if (!free.length) return;
    const i = free[Math.floor(rng() * free.length)];
    st.food = { x: i % this.cols, y: Math.floor(i / this.cols) };
  }

  /** Занята ли клетка. Хвост на этом ходу уйдёт, если змейка не растёт. */
  blocked(st, x, y, ignoreTail) {
    if (x < 0 || y < 0 || x >= this.cols || y >= this.rows) return true;
    if (!st.grid[y * this.cols + x]) return false;
    if (ignoreTail) {
      const t = st.snake[st.snake.length - 1];
      const foodAhead = (x === st.food.x && y === st.food.y);
      if (!foodAhead && t.x === x && t.y === y) return false;
    }
    return true;
  }

  sense(st) {
    const h = st.snake[0];
    const left = (st.dir + 3) % 4, right = (st.dir + 1) % 4, back = (st.dir + 2) % 4;
    const dangerAt = (d) => this.blocked(st, h.x + DIRS[d][0], h.y + DIRS[d][1], true);

    const dStraight = dangerAt(st.dir), dLeft = dangerAt(left), dRight = dangerAt(right);

    const wallDist = (d, x0, y0) => {
      let k = 1;
      while (!(x0 + DIRS[d][0] * k < 0 || y0 + DIRS[d][1] * k < 0 ||
               x0 + DIRS[d][0] * k >= this.cols || y0 + DIRS[d][1] * k >= this.rows)) k++;
      return Math.min(1, (k - 1) / Math.max(this.cols, this.rows));
    };

    let k = 1;
    for (; k <= 2; k++) {
      if (this.blocked(st, h.x + DIRS[st.dir][0] * k, h.y + DIRS[st.dir][1] * k, false)) break;
    }
    const freeAhead = (k - 1) / 2;

    let freeNeighbours = 0;
    for (let d = 0; d < 4; d++) if (!this.blocked(st, h.x + DIRS[d][0], h.y + DIRS[d][1], true)) freeNeighbours++;

    const dx = st.food.x - h.x, dy = st.food.y - h.y;
    const obs = new Float32Array(24);
    obs[0] = dStraight ? -1 : 1;
    obs[1] = dLeft ? -1 : 1;
    obs[2] = dRight ? -1 : 1;
    for (let d = 0; d < 4; d++) obs[3 + d] = d === st.dir ? 1 : -1;
    obs[7] = dx > 0 ? 1 : -1;
    obs[8] = dx < 0 ? 1 : -1;
    obs[9] = dy > 0 ? 1 : -1;
    obs[10] = dy < 0 ? 1 : -1;
    obs[11] = dx / this.cols;
    obs[12] = dy / this.rows;
    obs[13] = wallDist(st.dir, h.x, h.y);
    obs[14] = wallDist(left, h.x, h.y);
    obs[15] = wallDist(right, h.x, h.y);
    obs[16] = wallDist(back, h.x, h.y);
    obs[17] = st.snake.length / (this.cols * this.rows);
    obs[18] = (Math.abs(dx) + Math.abs(dy)) / (this.cols + this.rows);
    obs[19] = freeAhead;
    obs[20] = freeNeighbours / 4;
    // признаки кружения: недавно ли змейка занимала клетку перед каждым из трёх ходов
    obs[21] = this.recentVisit(st, h.x + DIRS[st.dir][0], h.y + DIRS[st.dir][1]) ? -1 : 1;
    obs[22] = this.recentVisit(st, h.x + DIRS[left][0], h.y + DIRS[left][1]) ? -1 : 1;
    obs[23] = this.recentVisit(st, h.x + DIRS[right][0], h.y + DIRS[right][1]) ? -1 : 1;

    const vert = dy > 0 ? 'ниже' : (dy < 0 ? 'выше' : '');
    const horz = dx > 0 ? 'правее' : (dx < 0 ? 'левее' : '');
    const foodRel = (vert || horz)
      ? `еда ${[vert, horz].filter(Boolean).join(' и ')}`
      : 'еда прямо по курсу';

    return { obs, dStraight, dLeft, dRight, foodRel };
  }

  decide(st, net) {
    const s = this.sense(st);
    const out = net.out(s.obs);
    let a = 0;
    for (let i = 1; i < 3; i++) if (out[i] > out[a]) a = i;
    const nd = a === 0 ? st.dir : (a === 1 ? (st.dir + 3) % 4 : (st.dir + 1) % 4);

    const parts = [s.dStraight ? 'впереди опасно' : 'впереди чисто'];
    if (s.dLeft) parts.push('слева опасно');
    if (s.dRight) parts.push('справа опасно');
    parts.push(s.foodRel);

    return {
      action: a,
      label: DIR_NAMES[nd],
      thought: `${parts.join(', ')} → движение ${DIR_NAMES[nd]}`,
      values: [out[0], out[1], out[2]],
      speak: true,
    };
  }

  step(st, act, rng) {
    const nd = act === 0 ? st.dir : (act === 1 ? (st.dir + 3) % 4 : (st.dir + 1) % 4);
    st.dir = nd;
    const h = st.snake[0];
    const nx = h.x + DIRS[nd][0], ny = h.y + DIRS[nd][1];
    const before = Math.abs(h.x - st.food.x) + Math.abs(h.y - st.food.y);

    const wall = nx < 0 || ny < 0 || nx >= this.cols || ny >= this.rows;
    const bite = !wall && this.blocked(st, nx, ny, true);

    st.stepCount++;
    if (wall || bite) {
      st.alive = false;
      st.deathReason = wall ? 'удар в стену' : 'укусил свой хвост';
      return { reward: -1.0, done: true };
    }

    // штраф за заход на клетку, где змейка была недавно: кружение не окупается
    const circling = this.recentVisit(st, nx, ny);
    let reward = -0.01 - (circling ? 0.15 : 0);

    st.snake.unshift({ x: nx, y: ny });
    st.grid[ny * this.cols + nx] = 1;
    st.lastVisit[ny * this.cols + nx] = st.stepCount;

    if (nx === st.food.x && ny === st.food.y) {
      reward += 1.0;                       // рост — главная цель
      st.eaten++;
      st.stepsSinceFood = 0;
      this.placeFood(st, rng);
    } else {
      const tail = st.snake.pop();
      st.grid[tail.y * this.cols + tail.x] = 0;
      st.stepsSinceFood++;
      const after = Math.abs(nx - st.food.x) + Math.abs(ny - st.food.y);
      reward += after < before ? 0.02 : -0.03;   // слабая подсказка направления
      if (st.stepsSinceFood > this.hungerLimit) {
        st.alive = false;
        st.deathReason = `голод: ${this.hungerLimit} шагов без яблока`;
        return { reward: reward - 0.5, done: true };
      }
    }
    return { reward, done: false };
  }

  score(st) { return st.snake.length; }

  /** Компактное состояние для страницы (по сети typed arrays не передаются). */
  view(st) {
    return {
      snake: st.snake.map((s) => [s.x, s.y]),
      food: [st.food.x, st.food.y],
      dir: st.dir,
      alive: st.alive,
      eaten: st.eaten,
      len: st.snake.length,
      stepsSinceFood: st.stepsSinceFood ?? 0,
    };
  }

  /* ---------- интерфейс для Лая (модель на RTX 3060) ---------- */
  freeNeighboursAt(st, x, y) {
    let n = 0;
    for (let d = 0; d < 4; d++) {
      const nx = x + DIRS[d][0], ny = y + DIRS[d][1];
      if (nx < 0 || ny < 0 || nx >= this.cols || ny >= this.rows) continue;
      if (st.grid[ny * this.cols + nx]) continue;
      n++;
    }
    return n;
  }

  /** Сколько шагов по свободным клеткам от клетки до яблока (BFS). Тело — препятствие,
   *  хвост проходим: он уйдёт на следующем ходу. */
  pathLength(st, from, goal) {
    if (from.x === goal.x && from.y === goal.y) return 0;
    const seen = new Uint8Array(this.cols * this.rows);
    const tail = st.snake[st.snake.length - 1];
    const queue = [from];
    seen[from.y * this.cols + from.x] = 1;
    let head = 0, depth = 0;
    while (head < queue.length) {
      const levelSize = queue.length - head;
      depth++;
      for (let k = 0; k < levelSize; k++) {
        const cur = queue[head++];
        for (let d = 0; d < 4; d++) {
          const nx = cur.x + DIRS[d][0], ny = cur.y + DIRS[d][1];
          if (nx < 0 || ny < 0 || nx >= this.cols || ny >= this.rows) continue;
          const i = ny * this.cols + nx;
          if (seen[i]) continue;
          const isTail = (tail.x === nx && tail.y === ny);
          if (st.grid[i] && !isTail) continue;
          if (nx === goal.x && ny === goal.y) return depth;
          seen[i] = 1;
          queue.push({ x: nx, y: ny });
        }
      }
    }
    return Infinity;
  }

  /** Сколько свободных клеток доступно из позиции (оценка простора, ограничено 144). */
  freeSpace(st, from) {
    const seen = new Uint8Array(this.cols * this.rows);
    const tail = st.snake[st.snake.length - 1];
    const queue = [from];
    seen[from.y * this.cols + from.x] = 1;
    let count = 0, head = 0;
    while (head < queue.length && count < 144) {
      const cur = queue[head++];
      count++;
      for (let d = 0; d < 4; d++) {
        const nx = cur.x + DIRS[d][0], ny = cur.y + DIRS[d][1];
        if (nx < 0 || ny < 0 || nx >= this.cols || ny >= this.rows) continue;
        const i = ny * this.cols + nx;
        if (seen[i]) continue;
        const isTail = (tail.x === nx && tail.y === ny);
        if (st.grid[i] && !isTail) continue;
        seen[i] = 1;
        queue.push({ x: nx, y: ny });
      }
    }
    return count;
  }

  /** Ситуация и варианты хода для Лая.
   *
   *  Замеры показали: модель Laya не «видит» координатную сетку (на бинарных вопросах
   *  о змейке её точность около случайной), поэтому грубые ошибки отсекают правила игры:
   *  смертельные ходы исключаются, а Лаю предлагаются ТРИ осмысленных безопасных хода —
   *  кратчайший путь к яблоку, ход с наибольшим простором и обходной путь.
   *  Лай выбирает между ними, то есть решения принимает он, а катастрофу не допускают правила. */
  /* ============ схема решений Лая, как в laya-decision-brain ============
     Короткие метки ходов (EAT / HUNT / DRIFT / TRAP / DEATH / ILLEGAL),
     предохранитель (seatbelt) и уроки из прошлых партий: они подаются
     модели в состоянии и меняют её правило (rule). */

  /** Что в клетке при ходе в абсолютном направлении d (0 вверх, 1 вправо, 2 вниз, 3 влево). */
  peekDir(st, d) {
    const h = st.snake[0];
    if (d === (st.dir + 2) % 4) return 'illegal';        // разворот на 180°
    const nx = h.x + DIRS[d][0], ny = h.y + DIRS[d][1];
    if (nx < 0 || ny < 0 || nx >= this.cols || ny >= this.rows) return 'wall';
    if (nx === st.food.x && ny === st.food.y) return 'food';
    if (this.blocked(st, nx, ny, true)) return 'body';
    return 'empty';
  }

  /** Сколько свободных клеток останется после хода (0 — ход смертелен). */
  reachableAfter(st, d) {
    const cell = this.peekDir(st, d);
    if (cell === 'wall' || cell === 'body' || cell === 'illegal') return 0;
    const h = st.snake[0];
    return this.freeSpace(st, { x: h.x + DIRS[d][0], y: h.y + DIRS[d][1] });
  }

  spaceNeeded(st, caution = 0) {
    return st.snake.length + Math.min(10, caution * 2);
  }

  /** Полная аналитика хода — основа и меток, и предохранителя. */
  describeMove(st, d, need) {
    const h = st.snake[0];
    const next = { x: h.x + DIRS[d][0], y: h.y + DIRS[d][1] };
    const cell = this.peekDir(st, d);
    const reverse = cell === 'illegal';
    const now = Math.abs(h.x - st.food.x) + Math.abs(h.y - st.food.y);
    const inbox = next.x >= 0 && next.y >= 0 && next.x < this.cols && next.y < this.rows;
    const then = inbox ? Math.abs(next.x - st.food.x) + Math.abs(next.y - st.food.y) : now + 99;
    const space = reverse || cell === 'wall' || cell === 'body' ? 0 : this.reachableAfter(st, d);
    return {
      d, next, cell, reverse,
      closer: then < now, farther: then > now, dist: then,
      space, trap: space > 0 && space < need,
    };
  }

  labelOf(m) {
    if (m.reverse) return 'ILLEGAL';
    if (m.cell === 'wall' || m.cell === 'body') return 'DEATH';
    if (m.trap) return 'TRAP';
    if (m.cell === 'food') return 'EAT';
    if (m.closer) return 'HUNT';
    return 'DRIFT';
  }

  safeDirs(st) {
    return [0, 1, 2, 3].filter((d) => {
      const c = this.peekDir(st, d);
      return c === 'empty' || c === 'food';
    });
  }

  /** Абсолютное направление → относительное действие (прямо/влево/вправо). */
  toAction(st, d) {
    if (d === st.dir) return 0;
    if (d === (st.dir + 3) % 4) return 1;
    if (d === (st.dir + 1) % 4) return 2;
    return 0;
  }

  /** Очевидный ход: если он один, Лая не спрашивают вообще (как в референсе). */
  obviousMove(st, caution = 0) {
    const safe = this.safeDirs(st);
    if (!safe.length) return null;
    const need = this.spaceNeeded(st, caution);
    let dir = null, reason = '';
    if (safe.length === 1) {
      dir = safe[0];
      reason = 'единственный безопасный ход';
    } else {
      const scored = safe.map((d) => this.describeMove(st, d, need));
      const eat = scored.filter((m) => m.cell === 'food');
      const hunt = scored.filter((m) => m.closer && !m.trap);
      if (eat.length === 1) { dir = eat[0].d; reason = 'единственный EAT — яблоко прямо по курсу'; }
      else if (hunt.length === 1) { dir = hunt[0].d; reason = 'единственный HUNT без кармана'; }
    }
    if (dir === null) return null;
    const m = this.describeMove(st, dir, need);
    return {
      action: this.toAction(st, dir),
      label: DIR_NAMES[dir],
      mark: this.labelOf(m),
      thought: `предохранитель: ${reason} (${this.labelOf(m)})`,
    };
  }

  /** Партия окончена? */
  isOver(st) { return !st.alive; }

  /** Предохранитель: выбор Лая проверяется правилами и при опасности заменяется. */
  resolveMove(st, wantedDir, probs, caution = 0) {
    const safe = this.safeDirs(st);
    if (!safe.length) {
      return { action: this.toAction(st, wantedDir), dir: wantedDir, overridden: false, reason: null, mark: 'DEATH' };
    }
    const need = this.spaceNeeded(st, caution);
    const scored = safe.map((m) => ({ ...this.describeMove(st, m, need), p: probs?.[m] ?? 0 }));
    const open = scored.filter((m) => !m.trap);
    const base = open.length ? open : scored;
    const eat = base.filter((m) => m.cell === 'food');
    const hunt = base.filter((m) => m.closer);
    const pool = eat.length ? eat : (hunt.length ? hunt : base);
    const ranked = [...pool].sort((a, b) => b.space - a.space || b.p - a.p);
    const dir = ranked[0].d;
    if (dir === wantedDir) {
      return { action: this.toAction(st, dir), dir, overridden: false, reason: null, mark: this.labelOf(ranked[0]) };
    }
    const picked = scored.find((m) => m.d === wantedDir);
    const cell = this.peekDir(st, wantedDir);
    const why = !safe.includes(wantedDir)
      ? (cell === 'illegal' ? 'Лай выбрал разворот (ILLEGAL)' : `Лай выбрал смертельный ход (${cell === 'wall' ? 'стена' : 'своё тело'})`)
      : picked?.trap ? `Лай выбрал карман (TRAP: осталось бы ${picked.space} клеток)`
      : (picked && !picked.closer && hunt.length) ? 'Лай ушёл от яблока (DRIFT), хотя был HUNT'
      : 'Лай оставил меньше свободного места';
    return {
      action: this.toAction(st, dir), dir, overridden: true, reason: why,
      mark: this.labelOf(ranked[0]), wanted: DIR_NAMES[wantedDir], got: DIR_NAMES[dir], caution,
    };
  }

  /** Урок из завершившейся партии — им Лай пользуется в следующих играх. */
  makeLesson(st, n, score) {
    const h = st.snake[0];
    const blocked = [0, 1, 2, 3].filter((d) => {
      const c = this.peekDir(st, d);
      return c === 'wall' || c === 'body';
    }).length;
    const wall = (st.deathReason || '').startsWith('удар');
    return {
      n, score, death: wall ? 'wall' : 'body', length: st.snake.length, at: `${h.x},${h.y}`,
      note: wall
        ? `партия ${n}: длина ${st.snake.length}, счёт ${score} — удар в стену на (${h.x},${h.y}). Держись дальше от края, если яблока там нет.`
        : `партия ${n}: длина ${st.snake.length}, счёт ${score} — укус своего тела на (${h.x},${h.y}), занято ${blocked}/4 сторон. Не закрывай карман, держи коридор открытым.`,
    };
  }

  /** Состояние для модели: компактная строка (наш сервис Laya принимает state строкой)
   *  + уроки из прошлых партий — ими Лай и пользуется. */
  layaState(st, career) {
    const h = st.snake[0];
    const dx = st.food.x - h.x, dy = st.food.y - h.y;
    const boxed = (career?.lessons || []).some((l) => l.death === 'body');
    const adj = ['вверх', 'вправо', 'вниз', 'влево'].map((name, d) => {
      const c = this.peekDir(st, d);
      const what = c === 'food' ? 'яблоко' : c === 'illegal' ? 'разворот' : c === 'wall' ? 'стена' : c === 'body' ? 'тело' : 'свободно';
      return `${name}: ${what}`;
    }).join(', ');
    const apple = [dy < 0 ? `${-dy} вверх` : dy > 0 ? `${dy} вниз` : '', dx < 0 ? `${-dx} влево` : dx > 0 ? `${dx} вправо` : ''].filter(Boolean).join(' ') || 'здесь';
    const hunt = [dy < 0 ? 'вверх' : '', dy > 0 ? 'вниз' : '', dx < 0 ? 'влево' : '', dx > 0 ? 'вправо' : ''].filter(Boolean).join(' ') || 'нет';
    const lesson = (career?.lessons || [])[0];
    return `Змейка ${this.cols}x${this.rows}. Длина ${st.snake.length}, свободных клеток ${this.cols * this.rows - st.snake.length}. ` +
      `Курс: ${DIR_NAMES[st.dir]}. Яблоко: ${apple}. Охота: ${hunt}. Вокруг головы: ${adj}. ` +
      `Правило: ${boxed ? 'избегай карманов TRAP, держи коридор открытым' : 'охоться за яблоком'}. ` +
      `Партий сыграно: ${career?.games ?? 0}, лучший счёт: ${career?.best ?? 0}.` +
      (lesson ? ` Урок прошлой партии: ${lesson.note.slice(0, 170)}` : '');
  }

  /** Вопрос-выбор: четыре абсолютных направления с метками (схема референса). */
  layaSituation(st, career, caution = 0) {
    const need = this.spaceNeeded(st, caution);
    const criteria = {};
    const options = [];
    ['вверх', 'вправо', 'вниз', 'влево'].forEach((name, d) => {
      const m = this.describeMove(st, d, need);
      const label = this.labelOf(m);
      criteria[name] =
        label === 'ILLEGAL' ? 'ILLEGAL. Это разворот на 180° в свою шею.'
        : label === 'DEATH' ? (m.cell === 'wall' ? 'DEATH. Удар в стену.' : 'DEATH. Удар в своё тело.')
        : label === 'TRAP' ? `TRAP. Останется всего ${m.space} свободных клеток — змейка запрётся сама. Никогда не выбирай это.`
        : label === 'EAT' ? `EAT. Яблоко здесь, останется ${m.space} свободных клеток. Выбирай это.`
        : label === 'HUNT' ? `HUNT. Ближе к яблоку (останется ${m.dist}), свободно ${m.space} клеток. Выбирай самый просторный HUNT.`
        : `DRIFT. Дальше от яблока (останется ${m.dist}), свободно ${m.space} клеток. Избегай, если есть EAT или HUNT.`;
      options.push({ label: name, action: this.toAction(st, d), dir: d, mark: label, dist: m.dist, space: m.space, trap: m.trap });
    });

    return {
      mode: 'choice',
      key: `snake:${st.dir}:${st.snake[0].x}:${st.snake[0].y}:${st.food.x}:${st.food.y}:${st.snake.length}`,
      state: this.layaState(st, career),
      questions: {
        move: {
          type: 'choice',
          instructions: 'Змейка. Выбери EAT; если его нет — HUNT; никогда не выбирай DEATH, ILLEGAL и TRAP.',
          criteria,
        },
      },
      options,
      marks: Object.fromEntries(options.map((o) => [o.label, o.mark])),
    };
  }

  render(ctx, st) {
    const { w, h } = this.viewport;
    const cell = Math.floor(Math.min(w / this.cols, h / this.rows));
    ctx.clearRect(0, 0, w, h);
    ctx.fillStyle = '#070b14';
    ctx.fillRect(0, 0, w, h);

    ctx.strokeStyle = 'rgba(80,120,200,0.13)';
    ctx.lineWidth = 1;
    for (let x = 0; x <= this.cols; x++) {
      ctx.beginPath(); ctx.moveTo(x * cell + 0.5, 0); ctx.lineTo(x * cell + 0.5, this.rows * cell); ctx.stroke();
    }
    for (let y = 0; y <= this.rows; y++) {
      ctx.beginPath(); ctx.moveTo(0, y * cell + 0.5); ctx.lineTo(this.cols * cell, y * cell + 0.5); ctx.stroke();
    }

    const fx = st.food.x * cell + cell / 2, fy = st.food.y * cell + cell / 2;
    ctx.save();
    ctx.shadowColor = '#ff4d6d'; ctx.shadowBlur = 12;
    ctx.fillStyle = '#ff4d6d';
    ctx.beginPath(); ctx.arc(fx, fy, cell * 0.33, 0, Math.PI * 2); ctx.fill();
    ctx.restore();

    for (let i = st.snake.length - 1; i >= 0; i--) {
      const s = st.snake[i];
      const t = i / Math.max(1, st.snake.length - 1);
      const head = i === 0;
      ctx.save();
      if (head) { ctx.shadowColor = '#4dd6ff'; ctx.shadowBlur = 14; }
      ctx.fillStyle = head ? '#8ef2ff' : `rgba(70,${190 - Math.floor(t * 90)},${255 - Math.floor(t * 40)},${0.95 - t * 0.45})`;
      const pad = head ? 1 : 2 + t * 2;
      ctx.fillRect(s.x * cell + pad, s.y * cell + pad, cell - pad * 2, cell - pad * 2);
      ctx.restore();
    }
  }
}

/* ================================================================== */
/* САПЁР                                                               */
/* ================================================================== */
export class MinesEngine {
  constructor() {
    this.id = 'mines';
    this.title = 'Сапёр';
    this.subtitle = 'классика 9×9 с 10 минами: числа по соседям 3×3, первый ход открывает пустую зону, победа — открыть все безопасные клетки · мозг: окно 5×5 → 12 нейронов';
    this.sizes = [28, 12, 1];
    this.population = 30;
    this.mutationRate = 0.14;
    this.mutationAmp = 0.4;
    this.cols = 9;
    this.rows = 9;
    this.mineCount = 10;
    this.maxSteps = 200;
    this.series = 2;
    this.scoreBonus = 0;
    this.maxCandidates = 18;
    this.viewport = { w: 288, h: 288 };
    this._feat = new Float32Array(this.sizes[0]);
  }

  newState() {
    const n = this.cols * this.rows;
    return {
      mine: new Uint8Array(n), open: new Uint8Array(n), flag: new Uint8Array(n),
      num: new Int8Array(n).fill(-1),
      placed: false, boom: false, win: false, first: true,
      opened: 0, flagged: 0,
    };
  }

  _place(st, rng, safeIdx) {
    const n = this.cols * this.rows;
    // Как в классическом «Сапёре»: первый клик открывает пустую зону,
    // поэтому мины не ставим ни в саму клетку, ни в её восьмёрку соседей.
    const forbidden = new Set([safeIdx, ...this._neighbours(safeIdx)]);
    let placed = 0;
    let guard = 0;
    while (placed < this.mineCount && guard++ < 10000) {
      const i = Math.floor(rng() * n);
      if (forbidden.has(i) || st.mine[i]) continue;
      st.mine[i] = 1; placed++;
    }
    for (let y = 0; y < this.rows; y++) {
      for (let x = 0; x < this.cols; x++) {
        const i = y * this.cols + x;
        if (st.mine[i]) { st.num[i] = -1; continue; }
        let c = 0;
        for (let dy = -1; dy <= 1; dy++) {
          for (let dx = -1; dx <= 1; dx++) {
            const nx = x + dx, ny = y + dy;
            if (nx < 0 || ny < 0 || nx >= this.cols || ny >= this.rows) continue;
            if (st.mine[ny * this.cols + nx]) c++;
          }
        }
        st.num[i] = c;
      }
    }
    st.placed = true;
  }

  _neighbours(i) {
    const x = i % this.cols, y = Math.floor(i / this.cols), out = [];
    for (let dy = -1; dy <= 1; dy++) {
      for (let dx = -1; dx <= 1; dx++) {
        if (!dx && !dy) continue;
        const nx = x + dx, ny = y + dy;
        if (nx < 0 || ny < 0 || nx >= this.cols || ny >= this.rows) continue;
        out.push(ny * this.cols + nx);
      }
    }
    return out;
  }

  _knownMines(st) {
    const res = new Set();
    for (let i = 0; i < st.open.length; i++) {
      if (!st.open[i] || st.num[i] <= 0) continue;
      const nb = this._neighbours(i);
      let flagged = 0; const hidden = [];
      for (const j of nb) { if (st.flag[j]) flagged++; else if (!st.open[j]) hidden.push(j); }
      if (hidden.length && flagged === st.num[i]) for (const j of hidden) res.add(j);
    }
    return [...res];
  }

  _safeCells(st) {
    const res = new Set();
    for (let i = 0; i < st.open.length; i++) {
      if (!st.open[i] || st.num[i] <= 0) continue;
      const nb = this._neighbours(i);
      let flagged = 0; const hidden = [];
      for (const j of nb) { if (st.flag[j]) flagged++; else if (!st.open[j]) hidden.push(j); }
      if (hidden.length && flagged + hidden.length === st.num[i]) for (const j of hidden) res.add(j);
    }
    return [...res].filter((i) => !st.flag[i] && !st.open[i]);
  }

  _frontier(st) {
    const set = new Set();
    for (let i = 0; i < st.open.length; i++) {
      if (!st.open[i]) continue;
      for (const j of this._neighbours(i)) if (!st.open[j] && !st.flag[j]) set.add(j);
    }
    return [...set];
  }

  /** Признаки клетки (переиспользуемый буфер): окно 5×5 + 3 глобальных признака. */
  _features(st, idx) {
    const obs = this._feat;
    const x = idx % this.cols, y = Math.floor(idx / this.cols);
    let k = 0;
    for (let dy = -2; dy <= 2; dy++) {
      const ny = y + dy;
      for (let dx = -2; dx <= 2; dx++) {
        const nx = x + dx;
        let v;
        if (nx < 0 || ny < 0 || nx >= this.cols || ny >= this.rows) v = 0.6;
        else {
          const j = ny * this.cols + nx;
          if (st.open[j]) v = st.num[j] / 8;
          else if (st.flag[j]) v = -1;
          else v = -0.2;
        }
        obs[k++] = v;
      }
    }
    const hidden = st.open.length - st.opened - st.flagged;
    obs[k++] = st.opened / (st.cols * this.rows);
    obs[k++] = hidden > 0 ? (this.mineCount - st.flagged) / hidden : 0;
    obs[k++] = st.flagged / this.mineCount;
    return obs;
  }

  _xy(idx) { return `(${(idx % this.cols) + 1},${Math.floor(idx / this.cols) + 1})`; }

  /** Тривиальные ходы (первый ход, гарантированные мины и безопасные клетки) —
   *  их агент делает сам, не спрашивая модель. */
  reflexMove(st, rng) {
    const n = this.cols * this.rows;
    if (st.first) {
      st.first = false;
      const idx = Math.floor(this.rows / 2) * this.cols + Math.floor(this.cols / 2);
      this._place(st, rng, idx);
      return { action: { type: 'open', idx }, label: `открыть ${this._xy(idx)}`,
        thought: 'первый ход: открываю центр — там мины быть не может' };
    }
    const mines = this._knownMines(st);
    if (mines.length) {
      return { action: { type: 'flag', list: mines }, label: `флаги ×${mines.length}`,
        thought: `по числам вычислил гарантированные мины (${mines.slice(0, 4).map((i) => this._xy(i)).join(', ')}${mines.length > 4 ? '…' : ''}) → ставлю флаги` };
    }
    const safe = this._safeCells(st);
    if (safe.length) {
      const idx = safe[Math.floor(rng() * safe.length)];
      return { action: { type: 'open', idx }, label: `открыть ${this._xy(idx)}`,
        thought: `клетка ${this._xy(idx)} гарантированно безопасна по числам → открываю` };
    }
    if (st.opened >= n - this.mineCount) {
      return { action: { type: 'wait' }, label: 'доска разобрана', thought: 'ходов не осталось' };
    }
    return null;
  }

  decide(st, net, rng) {
    const n = this.cols * this.rows;

    const reflex = this.reflexMove(st, rng);
    if (reflex) return { ...reflex, speak: true, kind: 'reflex' };

    let front = this._frontier(st);
    let kind = 'net';
    if (!front.length) {
      const hidden = [];
      for (let i = 0; i < n; i++) if (!st.open[i] && !st.flag[i]) hidden.push(i);
      if (!hidden.length) {
        return { action: { type: 'wait' }, label: 'нет ходов', thought: 'доска разобрана', speak: false };
      }
      front = hidden; kind = 'guess';
    }
    const totalCandidates = front.length;
    if (front.length > this.maxCandidates) front = front.slice(0, this.maxCandidates);

    let bestIdx = front[0], bestP = Infinity;
    for (const idx of front) {
      const v = net.scalar(this._features(st, idx));
      const p = 1 / (1 + Math.exp(-v));            // сигмоида: значение → вероятность мины
      if (p < bestP) { bestP = p; bestIdx = idx; }
    }
    const hiddenLeft = n - st.opened - st.flagged;
    const threat = hiddenLeft > 0 ? (this.mineCount - st.flagged) / hiddenLeft : 0;

    const thought = kind === 'net'
      ? `оценил ${totalCandidates} клеток фронта: минимальный риск ${(bestP * 100).toFixed(1)}% у ${this._xy(bestIdx)} (средняя угроза ${(threat * 100).toFixed(1)}%) → открываю`
      : `гарантированных ходов нет: открываю ${this._xy(bestIdx)} вслепую (риск ${(bestP * 100).toFixed(1)}%)`;

    return {
      action: { type: 'open', idx: bestIdx }, label: `открыть ${this._xy(bestIdx)}`,
      thought, speak: true, kind, risk: bestP, candidates: totalCandidates,
    };
  }

  _openFlood(st, start) {
    let count = 0;
    const stack = [start];
    while (stack.length) {
      const i = stack.pop();
      if (st.open[i] || st.flag[i] || st.mine[i]) continue;
      st.open[i] = 1; st.opened++; count++;
      if (st.num[i] === 0) for (const j of this._neighbours(i)) if (!st.open[j] && !st.flag[j]) stack.push(j);
    }
    return count;
  }

  step(st, action) {
    if (!action || action.type === 'wait') return { reward: -0.05, done: true };

    if (action.type === 'flag') {
      let added = 0;
      for (const i of action.list) if (!st.flag[i] && !st.open[i]) { st.flag[i] = 1; st.flagged++; added++; }
      return { reward: 0.02 * added, done: false };
    }

    const idx = action.idx;
    if (st.open[idx] || st.flag[idx]) return { reward: -0.02, done: false };
    if (st.mine[idx]) {
      st.boom = true;
      st.boomAt = idx;              // для урока: где именно подорвались
      return { reward: -12, done: true };
    }
    const gained = this._openFlood(st, idx);
    let reward = 0.1 * gained;
    const totalSafe = this.cols * this.rows - this.mineCount;
    if (st.opened >= totalSafe) {
      st.win = true;
      return { reward: reward + 25, done: true };
    }
    return { reward, done: false };
  }

  score(st) { return st.opened; }

  /** Состояние поля для страницы. */
  view(st) {
    return {
      open: Array.from(st.open),
      mine: Array.from(st.mine),
      flag: Array.from(st.flag),
      num: Array.from(st.num),
      boom: st.boom,
      win: st.win,
      opened: st.opened,
      flagged: st.flagged,
    };
  }

  /* ============ схема решений Лая (как в референсе) ============ */
  _infoScore(st, idx) {
    let openN = 0;
    for (const j of this._neighbours(idx)) if (st.open[j]) openN++;
    return openN;
  }

  /** Локальный риск клетки по числам открытых соседей: (мины рядом − флаги) / скрытых рядом. */
  localRisk(st, idx) {
    let best = null, info = 0;
    for (const j of this._neighbours(idx)) {
      if (!st.open[j] || st.num[j] <= 0) continue;
      info++;
      let mines = 0, hidden = 0;
      for (const k of this._neighbours(j)) {
        if (st.flag[k]) mines++;
        else if (!st.open[k]) hidden++;
      }
      if (!hidden) continue;
      const risk = Math.max(0, st.num[j] - mines) / hidden;
      if (best === null || risk < best) best = risk;
    }
    return { risk: best, info };
  }

  /** Метки клетки: SAFE / LOW / RISK / MINE / GUESS / BLIND. */
  cellMark(st, idx) {
    const { risk, info } = this.localRisk(st, idx);
    if (risk === null) return info > 0 ? 'GUESS' : 'BLIND';
    if (risk <= 0.0001) return 'SAFE';
    if (risk < 0.34) return 'LOW';
    if (risk < 0.67) return 'RISK';
    return 'MINE';
  }

  /** Приоритет метки: чем меньше, тем безопаснее. */
  _markRank(mark) {
    return { SAFE: 0, LOW: 1, GUESS: 2, RISK: 3, BLIND: 4, MINE: 5 }[mark] ?? 4;
  }

  /** Очевидный ход сапёра: если правила уже всё решили, Лая не спрашивают. */
  obviousMove(st, caution = 0, rng = Math.random) {
    const reflex = this.reflexMove(st, rng);
    if (!reflex) return null;
    return { ...reflex, mark: 'SAFE' };
  }

  /** Партия окончена? */
  isOver(st) { return !!st.boom || !!st.win; }

  /** Предохранитель: вариант Лая заменяется, если есть заметно более безопасная клетка. */
  resolveMove(st, wantedIdx) {
    const front = this._candidates(st);
    if (!front.length) return { idx: wantedIdx, overridden: false, reason: null, mark: 'BLIND' };
    const scored = front.map((idx) => {
      const mark = this.cellMark(st, idx);
      return { idx, mark, rank: this._markRank(mark), info: this._infoScore(st, idx), risk: this.localRisk(st, idx).risk };
    });
    scored.sort((a, b) => a.rank - b.rank || b.info - a.info || a.idx - b.idx);
    const best = scored[0];
    const picked = scored.find((s) => s.idx === wantedIdx);
    if (!picked || best.idx === wantedIdx) return { idx: wantedIdx, overridden: false, reason: null, mark: picked ? picked.mark : 'BLIND' };
    if (picked.rank <= best.rank) return { idx: wantedIdx, overridden: false, reason: null, mark: picked.mark };
    const why = picked.mark === 'MINE'
      ? 'Лай выбрал клетку, где логика чисел указывает на мину'
      : picked.mark === 'RISK'
        ? 'Лай выбрал клетку с высоким риском, хотя рядом есть более надёжная'
        : 'Лай выбрал менее информативную клетку';
    return { idx: best.idx, overridden: true, reason: why, mark: best.mark, wanted: picked.mark, got: best.mark };
  }

  _candidates(st, limit = 4) {
    let front = this._frontier(st);
    if (!front.length) {
      front = [];
      for (let i = 0; i < st.open.length; i++) if (!st.open[i] && !st.flag[i]) front.push(i);
    }
    const scored = front.map((idx) => ({ idx, mark: this.cellMark(st, idx), info: this._infoScore(st, idx) }));
    scored.sort((a, b) => this._markRank(a.mark) - this._markRank(b.mark) || b.info - a.info || a.idx - b.idx);
    return scored.slice(0, limit).map((s) => s.idx);
  }

  /** Урок из подрыва — Лай пользуется им в следующих партиях. */
  makeLesson(st, n, score) {
    const x = st.boomAt != null ? (st.boomAt % this.cols) + 1 : '?';
    const y = st.boomAt != null ? Math.floor(st.boomAt / this.cols) + 1 : '?';
    const nums = st.boomAt != null
      ? this._neighbours(st.boomAt).filter((j) => st.open[j]).map((j) => st.num[j])
      : [];
    return {
      n, score, death: 'mine', at: `${x},${y}`,
      note: `партия ${n}: открыто ${score} клеток — подрыв на (${x},${y}), рядом были числа [${nums.join(',') || '—'}]. ` +
        'Не открывай клетку, если рядом мин больше, чем флагов: сначала ставь флаги и открывай только SAFE.',
    };
  }

  /** Состояние для модели — компактная строка + уроки прошлых партий. */
  layaState(st, career) {
    const hiddenLeft = st.open.length - st.opened - st.flagged;
    const gained = (career?.lessons || []).some((l) => l.death === 'mine');
    const lesson = (career?.lessons || [])[0];
    return `Сапёр ${this.cols}x${this.rows}, мин ${this.mineCount}. Открыто ${st.opened}, флагов ${st.flagged}, ` +
      `скрыто ${hiddenLeft}, неотмеченных мин ${this.mineCount - st.flagged}. ` +
      `Правило: ${gained ? 'открывай только SAFE и LOW, вслепую не угадывай' : 'открывай безопасные клетки по числам'}. ` +
      `Партий: ${career?.games ?? 0}, лучший результат: ${career?.best ?? 0}.` +
      (lesson ? ` Урок прошлой партии: ${lesson.note.slice(0, 170)}` : '');
  }

  /** Вопрос-выбор: кандидаты с метками SAFE / LOW / RISK / GUESS. */
  layaSituation(st, career) {
    const cand = this._candidates(st);
    if (!cand.length) return null;
    const hiddenLeft = st.open.length - st.opened - st.flagged;
    const minesLeft = this.mineCount - st.flagged;
    const criteria = {};
    const options = [];

    for (const idx of cand) {
      const x = idx % this.cols, y = Math.floor(idx / this.cols);
      const mark = this.cellMark(st, idx);
      const { risk, info } = this.localRisk(st, idx);
      const name = `клетка ${x + 1},${y + 1}`;
      const riskTxt = risk === null ? 'риск не оценить по числам' : `оценка риска ${(risk * 100).toFixed(0)}%`;
      criteria[name] =
        mark === 'SAFE' ? `${name}: SAFE. По числам мины здесь быть не может. Выбирай это.`
        : mark === 'LOW' ? `${name}: LOW. ${riskTxt}, открытых чисел рядом ${info}. Хороший ход.`
        : mark === 'RISK' ? `${name}: RISK. ${riskTxt} — высокий риск, избегай.`
        : mark === 'GUESS' ? `${name}: GUESS. ${riskTxt}, чисел рядом ${info}. Это угадывание.`
        : `${name}: BLIND. Рядом нет открытых чисел, ${riskTxt}. Последнее средство.`;
      options.push({ label: name, action: { type: 'open', idx }, idx, mark, risk, info });
    }

    return {
      mode: 'choice',
      key: `mines:${st.opened}:${st.flagged}:${cand.join(',')}`,
      state: this.layaState(st, career),
      questions: {
        move: {
          type: 'choice',
          instructions: 'Сапёр. Выбери SAFE; если SAFE нет — LOW; избегай RISK и не открывай вслепую, если есть надёжная клетка.',
          criteria,
        },
      },
      options,
      marks: Object.fromEntries(options.map((o) => [o.label, o.mark])),
    };
  }

  render(ctx, st) {
    const { w, h } = this.viewport;
    const cell = Math.floor(Math.min(w / this.cols, h / this.rows));
    ctx.clearRect(0, 0, w, h);
    ctx.fillStyle = '#070b14';
    ctx.fillRect(0, 0, w, h);

    for (let y = 0; y < this.rows; y++) {
      for (let x = 0; x < this.cols; x++) {
        const i = y * this.cols + x;
        const px = x * cell, py = y * cell;
        if (st.open[i]) {
          ctx.fillStyle = '#101a2e';
          ctx.fillRect(px + 1, py + 1, cell - 2, cell - 2);
          if (st.num[i] > 0) {
            const colors = ['', '#5b8cff', '#3fd28a', '#ff6b6b', '#b06bff', '#ffa94d', '#38d9d9', '#e0e0e0', '#9aa5b1'];
            ctx.fillStyle = colors[st.num[i]] || '#ccc';
            ctx.font = `bold ${Math.floor(cell * 0.62)}px ui-monospace, monospace`;
            ctx.textAlign = 'center'; ctx.textBaseline = 'middle';
            ctx.fillText(String(st.num[i]), px + cell / 2, py + cell / 2 + 1);
          }
        } else {
          ctx.fillStyle = '#1b2740';
          ctx.fillRect(px + 1, py + 1, cell - 2, cell - 2);
          ctx.strokeStyle = 'rgba(120,160,240,0.25)';
          ctx.strokeRect(px + 1.5, py + 1.5, cell - 3, cell - 3);
          if (st.flag[i]) {
            ctx.fillStyle = '#ff4d6d';
            ctx.font = `bold ${Math.floor(cell * 0.6)}px ui-monospace, monospace`;
            ctx.textAlign = 'center'; ctx.textBaseline = 'middle';
            ctx.fillText('⚑', px + cell / 2, py + cell / 2 + 1);
          }
        }
        if (st.boom && st.mine[i]) {
          ctx.fillStyle = 'rgba(255,77,109,0.75)';
          ctx.fillRect(px + 1, py + 1, cell - 2, cell - 2);
        }
      }
    }
  }
}

/* ================================================================== */
/* ТЕТРИС                                                              */
/* ================================================================== */
const SHAPES = {
  I: [[1, 1, 1, 1]],
  O: [[1, 1], [1, 1]],
  T: [[0, 1, 0], [1, 1, 1]],
  S: [[0, 1, 1], [1, 1, 0]],
  Z: [[1, 1, 0], [0, 1, 1]],
  J: [[1, 0, 0], [1, 1, 1]],
  L: [[0, 0, 1], [1, 1, 1]],
};
const TYPES = Object.keys(SHAPES);
// Стандартная таблица очков за убранные линии (Brick Game / классический «Тетрис»):
// 1 линия — 100, 2 — 300, 3 — 700, 4 («тетрис») — 1500.
const LINE_POINTS = [0, 100, 300, 700, 1500];
const PIECE_COLORS = {
  I: '#4dd6ff', O: '#ffd166', T: '#b06bff', S: '#3fd28a',
  Z: '#ff6b6b', J: '#5b8cff', L: '#ffa94d',
};
const TYPE_INDEX = {};
TYPES.forEach((t, i) => { TYPE_INDEX[t] = i + 1; });

function rotateCW(m) {
  const rows = m.length, cols = m[0].length;
  const out = Array.from({ length: cols }, () => new Array(rows).fill(0));
  for (let y = 0; y < rows; y++) for (let x = 0; x < cols; x++) out[x][rows - 1 - y] = m[y][x];
  return out;
}

export class TetrisEngine {
  constructor() {
    this.id = 'tetris';
    this.title = 'Тетрис';
    this.subtitle = 'классика: стакан 10×20, тетрамино, очки 100/300/700/1500 за 1/2/3/4 линии · мозг: 23 признака поля → 16 нейронов';
    this.sizes = [23, 16, 1];
    this.population = 24;
    this.mutationRate = 0.16;
    this.mutationAmp = 0.35;
    this.cols = 10;
    this.rows = 20;
    this.maxSteps = 200;
    this.series = 2;
    this.scoreBonus = 0;
    this.viewport = { w: 200, h: 340 };

    this.rotations = {};
    for (const t of TYPES) {
      const list = [];
      let cur = SHAPES[t];
      for (let r = 0; r < 4; r++) {
        if (!list.some((m) => m.length === cur.length && m[0].length === cur[0].length &&
            m.every((row, i) => row.every((v, j) => v === cur[i][j])))) list.push(cur);
        cur = rotateCW(cur);
      }
      this.rotations[t] = list;
    }
    this._buf = new Uint8Array(this.cols * this.rows);
    this._feat = new Float32Array(this.sizes[0]);
    this._heights = new Int16Array(this.cols);
  }

  newState() {
    const st = { board: new Uint8Array(this.cols * this.rows), lines: 0, points: 0, pieces: 0, over: false, plan: null, piece: null };
    this._spawn(st, () => 0.37);
    return st;
  }

  _spawn(st, rng) {
    const type = TYPES[Math.floor(rng() * TYPES.length) % TYPES.length];
    const m = this.rotations[type][0];
    st.piece = { type, m, x: Math.floor((this.cols - m[0].length) / 2), y: 0 };
    if (this._collides(st.board, m, st.piece.x, 0)) st.over = true;
  }

  _collides(board, m, px, py) {
    const cols = this.cols, rows = this.rows;
    for (let y = 0; y < m.length; y++) {
      for (let x = 0; x < m[y].length; x++) {
        if (!m[y][x]) continue;
        const nx = px + x, ny = py + y;
        if (nx < 0 || nx >= cols || ny >= rows) return true;
        if (ny >= 0 && board[ny * cols + nx]) return true;
      }
    }
    return false;
  }

  _dropY(board, m, px) {
    let py = 0;
    if (this._collides(board, m, px, py)) return null;
    while (!this._collides(board, m, px, py + 1)) py++;
    return py;
  }

  /** Записать фигуру в буфер (буфер уже содержит копию поля). */
  _stamp(buf, m, px, py, val) {
    const cols = this.cols;
    for (let y = 0; y < m.length; y++) {
      for (let x = 0; x < m[y].length; x++) {
        if (!m[y][x]) continue;
        const ny = py + y;
        if (ny < 0) continue;
        buf[ny * cols + px + x] = val;
      }
    }
  }

  /** Признаки поля + снятие линий. Возвращает {cleared, agg, holes, wells, bump, maxH}. */
  _evaluate(buf) {
    const cols = this.cols, rows = this.rows;
    let cleared = 0;
    for (let y = 0; y < rows; y++) {
      let full = true;
      for (let x = 0; x < cols; x++) if (!buf[y * cols + x]) { full = false; break; }
      if (full) cleared++;
    }

    // высоты
    const heights = this._heights;
    let agg = 0, holes = 0, wells = 0, bump = 0, maxH = 0;
    for (let x = 0; x < cols; x++) {
      let hh = 0;
      let seen = false;
      for (let y = 0; y < rows; y++) {
        const v = buf[y * cols + x];
        if (v) { if (!seen) { seen = true; hh = rows - y; } }
        else if (seen) holes++;
      }
      heights[x] = hh;
      agg += hh;
      if (hh > maxH) maxH = hh;
      const l = x > 0 ? heights[x - 1] : rows;
      const r = x < cols - 1 ? heights[x + 1] : rows;
      const depth = Math.min(l, r) - hh;
      if (x > 0 && x < cols - 1 && depth > 0) wells += depth;
    }
    for (let x = 0; x < cols - 1; x++) bump += Math.abs(heights[x] - heights[x + 1]);
    return { cleared, agg, holes, wells, bump, maxH };
  }

  _features(stat, type) {
    const obs = this._feat;
    const heights = this._heights;
    let k = 0;
    for (let x = 0; x < this.cols; x++) obs[k++] = heights[x] / this.rows;
    obs[k++] = Math.min(1, stat.holes / 20);
    obs[k++] = stat.agg / (this.rows * this.cols);
    obs[k++] = stat.maxH / this.rows;
    obs[k++] = Math.min(1, stat.bump / 20);
    obs[k++] = Math.min(1, stat.wells / 20);
    obs[k++] = stat.cleared / 4;
    for (const t of TYPES) obs[k++] = t === type ? 1 : -1;
    return obs;
  }

  _simulateClear(buf) {
    // снять заполненные строки прямо в буфере
    const cols = this.cols, rows = this.rows;
    let write = rows - 1;
    for (let y = rows - 1; y >= 0; y--) {
      let full = true;
      for (let x = 0; x < cols; x++) if (!buf[y * cols + x]) { full = false; break; }
      if (full) continue;
      if (write !== y) for (let x = 0; x < cols; x++) buf[write * cols + x] = buf[y * cols + x];
      write--;
    }
    for (let y = write; y >= 0; y--) for (let x = 0; x < cols; x++) buf[y * cols + x] = 0;
  }

  decide(st, net) {
    if (st.over) {
      return { action: { type: 'none' }, label: 'конец', thought: 'стакан переполнен', speak: false };
    }
    if (!this._heights) this._heights = new Int16Array(this.cols);

    const type = st.piece.type;    const rots = this.rotations[type];
    const buf = this._buf;
    let best = null, tried = 0;

    for (let r = 0; r < rots.length; r++) {
      const m = rots[r];
      const w = m[0].length;
      for (let x = -2; x <= this.cols - w + 2; x++) {
        const py = this._dropY(st.board, m, x);
        if (py === null) continue;
        tried++;
        buf.set(st.board);
        this._stamp(buf, m, x, py, TYPE_INDEX[type]);
        const stat = this._evaluate(buf);
        const feats = this._features(stat, type);
        const v = net.scalar(feats);
        if (!best || v > best.v) {
          this._simulateClear(buf);
          best = { v, r, x, cleared: stat.cleared, buf: buf.slice() };
        }
      }
    }

    if (!best) {
      st.over = true;
      return { action: { type: 'none' }, label: 'конец', thought: 'ни одного размещения — стакан переполнен', speak: true };
    }

    st.plan = best;
    return {
      action: { type: 'place' },
      label: `колонка ${best.x + 1}, поворот ${best.r + 1}`,
      thought: `перебрал ${tried} размещений: лучшее — колонка ${best.x + 1}, поворот ${best.r + 1}, оценка ${best.v.toFixed(2)}, сгорит линий ${best.cleared}`,
      speak: true,
      candidates: tried,
    };
  }

  step(st, action, rng) {
    if (action && action.place) return this.playPlacement(st, action.place, rng);
    if (st.over || !st.plan) { st.over = true; return { reward: -6, done: true }; }
    const plan = st.plan;
    st.plan = null;
    return this.playPlacement(st, { r: plan.r, x: plan.x, cleared: plan.cleared, board: plan.buf }, rng);
  }

  /** Поставить фигуру по выбранному размещению (используется и сетью, и Лаем). */
  playPlacement(st, place, rng) {
    st.board = place.board;
    st.lines += place.cleared;
    st.points += LINE_POINTS[place.cleared] || 0;
    st.pieces++;

    const stat = this._evaluate(st.board);
    let reward = 0.35;                        // выживание — главный сигнал на старте обучения
    reward += (LINE_POINTS[place.cleared] || 0) / 100;   // очки по стандартной таблице
    reward -= 0.003 * stat.agg;               // высота стопки
    reward -= 0.05 * stat.holes;              // дыры
    reward -= 0.005 * stat.bump;              // неровность
    reward -= 0.015 * stat.wells;

    this._spawn(st, rng);
    if (st.over) return { reward: reward - 6, done: true };
    return { reward, done: false };
  }

  score(st) { return st.points; }             // рекорд считается в очках, как в классике
  linesOf(st) { return st.lines; }

  /** Состояние стакана для страницы. */
  view(st) {
    return {
      board: Array.from(st.board),
      piece: st.piece ? { type: st.piece.type, m: st.piece.m, x: st.piece.x, y: st.piece.y } : null,
      over: st.over,
      lines: st.lines,
      points: st.points,
      pieces: st.pieces,
    };
  }

  /* ============ схема решений Лая (как в референсе) ============ */
  /** Классическая оценочная функция тетриса. */
  _heuristic(stat) {
    return -0.510066 * stat.agg + 0.760666 * stat.cleared - 0.35663 * stat.holes - 0.184483 * stat.bump;
  }

  /** Все возможные размещения текущей фигуры с их статистикой. */
  placements(st) {
    if (st.over || !st.piece || !st.board) return [];
    const type = st.piece.type;
    const rots = this.rotations[type];
    const buf = this._buf;
    const out = [];
    for (let r = 0; r < rots.length; r++) {
      const m = rots[r];
      const w = m[0].length;
      for (let x = -2; x <= this.cols - w + 2; x++) {
        const py = this._dropY(st.board, m, x);
        if (py === null) continue;
        buf.set(st.board);
        this._stamp(buf, m, x, py, TYPE_INDEX[type]);
        const stat = this._evaluate(buf);
        this._simulateClear(buf);
        out.push({ r, x, py, stat, board: buf.slice(), h: this._heuristic(stat) });
      }
    }
    return out;
  }

  /** Метка размещения: CLEAR / FLAT / HOLES / TALL / DEATH. */
  placementMark(st, p) {
    const s = p.stat;
    if (s.maxH >= this.rows - 1) return 'DEATH';
    if (s.cleared > 0) return 'CLEAR';
    if (s.holes > 0) return 'HOLES';
    if (s.maxH >= this.rows - 5) return 'TALL';
    return 'FLAT';
  }

  _markRankPlacement(mark) {
    return { CLEAR: 0, FLAT: 1, TALL: 2, HOLES: 3, DEATH: 4 }[mark] ?? 3;
  }

  /** Очевидное размещение (внутренняя проверка). */
  obviousPlacement(st) {
    const places = this.placements(st);
    if (!places.length) return null;
    const marks = places.map((p) => ({ p, mark: this.placementMark(st, p) }));
    const clean = marks.filter((m) => m.mark === 'CLEAR');
    if (clean.length === 1) return clean[0].p;
    const flat = marks.filter((m) => m.mark === 'FLAT');
    if (marks.every((m) => m.mark === 'DEATH')) return places[0];
    if (places.length === 1) return places[0];
    if (flat.length === 1 && !clean.length) return flat[0].p;
    return null;
  }

  /** Очевидное размещение: единственный CLEAR или единственный вариант — без Лая. */
  obviousMove(st, caution = 0) {
    const p = this.obviousPlacement(st);
    if (!p) return null;
    const mark = this.placementMark(st, p);
    return {
      action: { type: 'place', place: { r: p.r, x: p.x, cleared: p.stat.cleared, board: p.board } },
      label: `колонка ${p.x + 1}, поворот ${p.r + 1}`,
      mark,
      thought: `предохранитель: размещение однозначно (${mark}, сгорит линий ${p.stat.cleared}, дыр ${p.stat.holes})`,
    };
  }

  /** Партия окончена? */
  isOver(st) { return !!st.over; }

  /** Предохранитель: если Лай выбрал размещение с дырами при наличии чистого — переопределяем. */
  resolvePlacement(st, chosen, all) {
    const marks = all.map((p) => ({ p, mark: this.placementMark(st, p) }));
    marks.sort((a, b) => this._markRankPlacement(a.mark) - this._markRankPlacement(b.mark) || b.p.h - a.p.h);
    const best = marks[0];
    const picked = marks.find((m) => m.p === chosen);
    if (!picked || best.p === chosen) return { place: chosen, overridden: false, reason: null, mark: picked ? picked.mark : 'FLAT' };
    if (this._markRankPlacement(picked.mark) <= this._markRankPlacement(best.mark)) {
      return { place: chosen, overridden: false, reason: null, mark: picked.mark };
    }
    const why = picked.mark === 'DEATH'
      ? 'Лай выбрал размещение, которое переполняет стакан (DEATH)'
      : picked.mark === 'HOLES'
        ? 'Лай выбрал размещение, создающее дыры (HOLES), хотя есть чистое'
        : 'Лай выбрал высокое размещение (TALL), хотя есть ниже';
    return { place: best.p, overridden: true, reason: why, mark: best.mark, wanted: picked.mark, got: best.mark };
  }

  /** Урок из проигранной партии. */
  makeLesson(st, n, score) {
    const stat = this._evaluate(st.board);
    return {
      n, score, death: 'over', at: `высота ${stat.maxH}`,
      note: `партия ${n}: очков ${score} — стакан переполнен при максимальной высоте ${stat.maxH}, дыр ${stat.holes}, неровность ${stat.bump}. ` +
        'Не оставляй дыры под фигурами и не копи высоту: сначала выравнивай поверхность, потом собирай линии.',
    };
  }

  /** Состояние для модели — компактная строка + уроки прошлых партий. */
  layaState(st, career) {
    const s = this._evaluate(st.board);
    const over = (career?.lessons || []).some((l) => l.death === 'over');
    const lesson = (career?.lessons || [])[0];
    return `Тетрис ${this.cols}x${this.rows}, фигура ${st.piece ? st.piece.type : '—'}. Линий ${st.lines}, очков ${st.points}. ` +
      `Высота: суммарно ${s.agg}, максимум ${s.maxH}; дыр ${s.holes}, неровность ${s.bump}, колодцев ${s.wells}. ` +
      `Правило: ${over ? 'не создавай дыры и держи поверхность ровной' : 'собирай линии и держи стакан низким'}. ` +
      `Партий: ${career?.games ?? 0}, лучший результат: ${career?.best ?? 0}.` +
      (lesson ? ` Урок прошлой партии: ${lesson.note.slice(0, 170)}` : '');
  }

  /** Вопрос-выбор: размещения с метками CLEAR / FLAT / TALL / HOLES / DEATH. */
  layaSituation(st, career) {
    const all = this.placements(st);
    if (!all.length) return null;

    const marks = all.map((p) => ({ p, mark: this.placementMark(st, p) }));
    marks.sort((a, b) => this._markRankPlacement(a.mark) - this._markRankPlacement(b.mark) || b.p.h - a.p.h);
    const top = marks.slice(0, 4);
    // детерминированный порядок вариантов, чтобы выбор Лая был однозначен
    const ordered = [...top].sort((a, b) => (b.p.r - a.p.r) || (b.p.x - a.p.x));

    const criteria = {};
    const options = [];
    for (const { p, mark } of ordered) {
      const name = `поворот ${p.r + 1}, колонка ${p.x + 1}`;
      criteria[name] =
        mark === 'DEATH' ? `${name}: DEATH. Стакан переполнится, партия закончится.`
        : mark === 'CLEAR' ? `${name}: CLEAR. Сгорит линий ${p.stat.cleared}, дыр станет ${p.stat.holes}. Выбирай это.`
        : mark === 'HOLES' ? `${name}: HOLES. Создаёт дыры (${p.stat.holes}), высота ${p.stat.maxH}. Избегай.`
        : mark === 'TALL' ? `${name}: TALL. Линий не будет, высота ${p.stat.maxH} — стакан почти полный.`
        : `${name}: FLAT. Ровно и без дыр, высота ${p.stat.maxH}, неровность ${p.stat.bump}. Хороший ход.`;
      options.push({ label: name, action: { type: 'place', place: { r: p.r, x: p.x, cleared: p.stat.cleared, board: p.board } }, mark, place: p, stat: p.stat });
    }
    void top;

    return {
      mode: 'choice',
      key: `tetris:${st.piece ? st.piece.type : '-'}:${st.pieces}:${Math.round(all[0].stat.maxH)}`,
      state: this.layaState(st, career),
      questions: {
        move: {
          type: 'choice',
          instructions: 'Тетрис. Выбери CLEAR; если CLEAR нет — FLAT; никогда не выбирай DEATH и HOLES.',
          criteria,
        },
      },
      options,
      marks: Object.fromEntries(options.map((o) => [o.label, o.mark])),
      all,
    };
  }

  render(ctx, st) {
    const { w, h } = this.viewport;
    const cell = Math.floor(Math.min(w / this.cols, h / this.rows));
    ctx.clearRect(0, 0, w, h);
    ctx.fillStyle = '#070b14';
    ctx.fillRect(0, 0, w, h);

    ctx.strokeStyle = 'rgba(80,120,200,0.10)';
    for (let x = 0; x <= this.cols; x++) {
      ctx.beginPath(); ctx.moveTo(x * cell + 0.5, 0); ctx.lineTo(x * cell + 0.5, this.rows * cell); ctx.stroke();
    }
    for (let y = 0; y <= this.rows; y++) {
      ctx.beginPath(); ctx.moveTo(0, y * cell + 0.5); ctx.lineTo(this.cols * cell, y * cell + 0.5); ctx.stroke();
    }

    const draw = (x, y, val, alpha = 1) => {
      ctx.globalAlpha = alpha;
      ctx.fillStyle = PIECE_COLORS[TYPES[val - 1]] || '#5b8cff';
      ctx.fillRect(x * cell + 1, y * cell + 1, cell - 2, cell - 2);
      ctx.globalAlpha = 1;
    };

    for (let y = 0; y < this.rows; y++) {
      for (let x = 0; x < this.cols; x++) {
        const v = st.board[y * this.cols + x];
        if (v) draw(x, y, v);
      }
    }

    if (st.piece && !st.over) {
      const m = st.piece.m;
      for (let y = 0; y < m.length; y++) {
        for (let x = 0; x < m[y].length; x++) {
          if (m[y][x] && st.piece.y + y >= 0) draw(st.piece.x + x, st.piece.y + y, TYPE_INDEX[st.piece.type], 0.85);
        }
      }
    }
  }
}

export const ENGINES = {
  snake: () => new SnakeEngine(),
  mines: () => new MinesEngine(),
  tetris: () => new TetrisEngine(),
};
