// brain.js — «мозг» Лая: нейросеть + движок нейроэволюции.
// Никаких заранее прописанных стратегий: агент обучается только эволюцией весов.

export function mulberry32(seed) {
  let a = seed >>> 0;
  return function () {
    a |= 0; a = (a + 0x6D2B79F5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

export function gauss(rng) {
  let u = 0, v = 0;
  while (u === 0) u = rng();
  while (v === 0) v = rng();
  return Math.sqrt(-2 * Math.log(u)) * Math.cos(2 * Math.PI * v);
}

export function mean(arr) {
  if (!arr.length) return 0;
  let s = 0;
  for (let i = 0; i < arr.length; i++) s += arr[i];
  return s / arr.length;
}

/* ------------------------------------------------------------------ */
/* Нейросеть: полносвязный перцептрон, активация tanh                  */
/* ------------------------------------------------------------------ */
export class Net {
  constructor(sizes) {
    this.sizes = sizes.slice();
    this.layers = [];
    for (let i = 1; i < sizes.length; i++) {
      const fan = sizes[i - 1], out = sizes[i];
      this.layers.push({
        fan, out,
        m: new Float32Array(fan * out),
        b: new Float32Array(out),
        _o: null,
      });
    }
  }

  static random(sizes, rng) {
    return new Net(sizes).init(rng);
  }

  init(rng) {
    for (const L of this.layers) {
      const lim = Math.sqrt(2 / L.fan);
      for (let k = 0; k < L.m.length; k++) L.m[k] = gauss(rng) * lim * 0.7;
      for (let k = 0; k < L.b.length; k++) L.b[k] = gauss(rng) * 0.05;
    }
    return this;
  }

  /** Прямой проход. x — массив/Float32Array длиной sizes[0].
   *  Скрытые слои — tanh, выходной слой линейный: так сети-оценщики
   *  (Сапёр, Тетрис) сохраняют различия между вариантами, а argmax
   *  для Змейки от монотонной активации не зависит. */
  out(x) {
    let cur = x;
    const last = this.layers.length - 1;
    for (let li = 0; li < this.layers.length; li++) {
      const L = this.layers[li];
      if (!L._o || L._o.length !== L.out) L._o = new Float32Array(L.out);
      const o = L._o, m = L.m, b = L.b, fan = L.fan;
      const linear = li === last;
      for (let j = 0; j < L.out; j++) {
        let s = b[j];
        const base = j * fan;
        for (let i = 0; i < fan; i++) s += m[base + i] * cur[i];
        o[j] = linear ? s : Math.tanh(s);
      }
      cur = o;
    }
    return cur;
  }

  /** Скалярный выход (используется там, где сеть оценивает вариант). */
  scalar(x) {
    return this.out(x)[0];
  }

  clone() {
    const n = new Net(this.sizes);
    for (let li = 0; li < this.layers.length; li++) {
      n.layers[li].m.set(this.layers[li].m);
      n.layers[li].b.set(this.layers[li].b);
    }
    return n;
  }

  mutate(rate, amp, rng) {
    for (const L of this.layers) {
      for (let k = 0; k < L.m.length; k++) {
        if (rng() < rate) L.m[k] += gauss(rng) * amp;
      }
      for (let k = 0; k < L.b.length; k++) {
        if (rng() < rate * 0.5) L.b[k] += gauss(rng) * amp * 0.5;
      }
    }
    return this;
  }

  static crossover(a, b, rng) {
    if (a.sizes.join() !== b.sizes.join()) return a.clone();
    const child = a.clone();
    for (let li = 0; li < a.layers.length; li++) {
      const cm = child.layers[li].m, am = a.layers[li].m, bm = b.layers[li].m;
      // смешанное наследование: блоками по 8 весов (сохраняет согласованные признаки)
      for (let k = 0; k < cm.length; k += 8) {
        const take = rng() < 0.5 ? am : bm;
        for (let j = k; j < Math.min(k + 8, cm.length); j++) cm[j] = take[j];
      }
    }
    return child;
  }

  serialize() {
    return {
      sizes: this.sizes,
      layers: this.layers.map((L) => ({ m: Array.from(L.m), b: Array.from(L.b) })),
    };
  }

  static deserialize(obj) {
    const n = new Net(obj.sizes);
    for (let li = 0; li < n.layers.length; li++) {
      n.layers[li].m.set(obj.layers[li].m);
      n.layers[li].b.set(obj.layers[li].b);
    }
    return n;
  }
}

/* ------------------------------------------------------------------ */
/* Популяция: все особи играют свои эпизоды одновременно               */
/* ------------------------------------------------------------------ */
export class Population {
  constructor(game, opts = {}) {
    this.game = game;
    this.rng = mulberry32(opts.seed ?? (game.id.length * 7919 + 13));
    this.size = opts.size ?? game.population ?? 40;
    this.eliteCount = Math.max(2, Math.round(this.size * (opts.eliteRatio ?? 0.1)));
    this.mutationRate = game.mutationRate ?? 0.15;
    this.mutationAmp = game.mutationAmp ?? 0.4;
    this.baseMutationRate = this.mutationRate;
    this.baseMutationAmp = this.mutationAmp;
    this.stall = 0;
    this.series = opts.series ?? game.series ?? 1;
    this.seedNet = opts.seedNet || null;

    this.generation = 1;
    this.episodes = 0;
    this.steps = 0;
    this.recordSteps = 0;      // шагов за последнюю секунду (для индикатора скорости)
    this._secSteps = 0;
    this._secAt = performance.now();

    this.history = [];
    this.bestScore = 0;
    this.avgScore = 0;
    this.bestFitness = 0;
    this.avgFitness = 0;
    this.bestEverNet = null;
    this.bestEverScore = 0;
    this.bestEverFitness = -Infinity;

    this.onDecision = null;
    this.onGeneration = null;
    this.onEpisode = null;

    this.focusIndex = 0;
    this.focusMode = 'best';   // 'best' | 'random'

    this.members = [];
    const half = Math.floor(this.size * 0.5);
    for (let i = 0; i < this.size; i++) {
      let net;
      if (this.seedNet && i < half) {
        // продолжение обучения: половина популяции — потомки сохранённой сети
        net = i === 0 ? this.seedNet.clone() : this.seedNet.clone().mutate(this.mutationRate, this.mutationAmp, this.rng);
      } else {
        net = Net.random(game.sizes, this.rng);
      }
      this.members.push({ net });
    }
    if (this.seedNet) {
      this.bestEverNet = this.seedNet.clone();
      this.bestEverScore = opts.seedScore || 0;
      this.bestEverFitness = -Infinity;
      this.bestScore = opts.seedScore || 0;
    }
    this.resetStates();
  }

  resetStates() {
    for (const m of this.members) {
      m.state = this.game.newState(this.rng);
      m.fitness = 0;
      m.finalFitness = 0;
      m.steps = 0;
      m.runs = 0;
      m.score = 0;
      m.done = false;
      m.lastDecision = null;
    }
    if (this.focusMode === 'random') {
      this.focusIndex = Math.floor(this.rng() * this.size);
    } else {
      this.focusIndex = 0;
    }
  }

  get focus() {
    return this.members[Math.min(this.focusIndex, this.members.length - 1)];
  }

  /** Один шаг для всех ещё живых особей. */
  tick() {
    let alive = 0;
    for (let i = 0; i < this.members.length; i++) {
      const m = this.members[i];
      if (m.done) continue;
      alive++;
      const d = this.game.decide(m.state, m.net, this.rng);
      m.lastDecision = d;
      const r = this.game.step(m.state, d.action, this.rng);
      if (i === this.focusIndex && this.onDecision) this.onDecision(d, m, this, r);
      m.fitness += r.reward;
      m.steps++;
      this.steps++;
      this._secSteps++;
      if (r.done || m.steps >= this.game.maxSteps) {
        const epScore = this.game.score(m.state);
        if (epScore > m.score) m.score = epScore;
        m.episodes = (m.episodes || 0) + 1;
        this.episodes++;
        if (m.runs + 1 < this.series) {
          // серия эпизодов: оценка особи устойчивее к случайности раздачи
          m.runs++;
          m.state = this.game.newState(this.rng);
          m.steps = 0;
        } else {
          m.done = true;
          m.finalFitness = m.fitness + m.score * (this.game.scoreBonus ?? 0);
          if (this.onEpisode) this.onEpisode(m, this);
        }
      }
    }
    if (alive === 0) this.evolve();

    const now = performance.now();
    if (now - this._secAt >= 1000) {
      this.recordSteps = Math.round((this._secSteps * 1000) / (now - this._secAt));
      this._secSteps = 0;
      this._secAt = now;
    }
  }

  tournament(sorted) {
    let best = null;
    for (let k = 0; k < 3; k++) {
      const c = sorted[Math.floor(this.rng() * sorted.length)];
      if (!best || c.finalFitness > best.finalFitness) best = c;
    }
    return best;
  }

  /** Смена поколения: элитизм → отбор → кроссовер → мутация → «свежая кровь». */
  evolve() {
    const sorted = this.members.slice().sort((a, b) => b.finalFitness - a.finalFitness);
    const best = sorted[0];
    const avgF = mean(sorted.map((m) => m.finalFitness));
    const avgS = mean(sorted.map((m) => m.score));

    if (best.score > this.bestScore) this.bestScore = best.score;
    this.bestFitness = best.finalFitness;
    this.avgFitness = avgF;
    this.avgScore = avgS;

    const gen = this.generation;
    this.history.push({
      gen,
      best: best.score,
      avg: +avgS.toFixed(2),
      bestFit: +best.finalFitness.toFixed(1),
      avgFit: +avgF.toFixed(1),
    });
    if (this.history.length > 130) this.history.shift();

    const improved = best.finalFitness > this.bestEverFitness;
    if (best.finalFitness >= this.bestEverFitness) {
      this.bestEverFitness = best.finalFitness;
      this.bestEverNet = best.net.clone();
      this.bestEverScore = best.score;
    }

    this.generation = gen + 1;

    const next = [];
    for (let i = 0; i < this.eliteCount; i++) next.push({ net: sorted[i].net.clone() });

    const fresh = Math.max(1, Math.round(this.size * 0.08));
    while (next.length < this.size - fresh) {
      const a = this.tournament(sorted);
      const b = this.tournament(sorted);
      const net = Net.crossover(a.net, b.net, this.rng)
        .mutate(this.mutationRate, this.mutationAmp, this.rng);
      next.push({ net });
    }
    while (next.length < this.size) {
      next.push({ net: this.seedNet ? Net.random(this.game.sizes, this.rng) : Net.random(this.game.sizes, this.rng) });
    }

    this.members = next;
    this.resetStates();

    // Управление мутацией по «застою»: пока рекорд растёт — держим точную настройку,
    // после 10 поколений без улучшения — короткая вспышка разнообразия и сброс.
    if (improved) {
      this.stall = 0;
      this.mutationRate = this.baseMutationRate;
      this.mutationAmp = this.baseMutationAmp;
    } else {
      this.stall++;
      if (this.stall >= 10) {
        this.stall = 0;
        this.mutationRate = Math.min(0.26, this.baseMutationRate * 1.7);
        this.mutationAmp = Math.min(0.75, this.baseMutationAmp * 1.6);
      }
    }

    if (this.onGeneration) {
      this.onGeneration({
        gen,
        best,
        avgF,
        avgS,
        nextGen: this.generation,
        mutationRate: this.mutationRate,
      }, this);
    }
  }
}
