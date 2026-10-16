// laya-player.mjs — серверный игрок-Лай со схемой laya-decision-brain:
//  1) предохранитель (seatbelt) решает очевидные ходы сам и не дёргает модель;
//  2) если ход неочевиден — Лай выбирает из вариантов с короткими метками
//     (EAT/HUNT/TRAP/DEATH/ILLEGAL для змейки, SAFE/LOW/RISK для сапёра, CLEAR/FLAT/HOLES для тетриса);
//  3) предохранитель перепроверяет выбор и при опасности переопределяет его с объяснением;
//  4) после каждой партии формируется УРОК, который попадает в «карьеру» и подаётся
//     модели в состоянии следующей партии — так Лай учится на своих же данных.
import http from 'node:http';

const agent = new http.Agent({ keepAlive: true, maxSockets: 16 });

/**
 * Приоритет меток по безопасности — от лучшей к худшей. Используется, когда
 * модель недоступна: ход выбирают правила, а не «ничего не делать».
 * Метки совпадают с теми, что отдают layaSituation в src/games.js.
 */
const SAFE_ORDER = {
  snake: ['EAT', 'HUNT', 'DRIFT', 'TRAP', 'DEATH', 'ILLEGAL'],
  mines: ['SAFE', 'LOW', 'RISK', 'GUESS', 'BLIND'],
  tetris: ['CLEAR', 'FLAT', 'TALL', 'HOLES', 'DEATH'],
};

function decide(port, payload) {
  return new Promise((resolve, reject) => {
    const body = Buffer.from(JSON.stringify(payload), 'utf8');
    const req = http.request({
      host: '127.0.0.1', port, path: '/decide', method: 'POST', agent,
      headers: { 'content-type': 'application/json', 'content-length': body.length },
      timeout: 20000,
    }, (res) => {
      const chunks = [];
      res.on('data', (c) => chunks.push(c));
      res.on('end', () => {
        if (res.statusCode !== 200) return reject(new Error('HTTP ' + res.statusCode));
        try { resolve(JSON.parse(Buffer.concat(chunks).toString('utf8'))); }
        catch (e) { reject(e); }
      });
    });
    req.on('error', reject);
    req.on('timeout', () => { req.destroy(); reject(new Error('timeout')); });
    req.write(body);
    req.end();
  });
}

const clock = () => new Date().toLocaleTimeString('ru-RU');

export class LayaPlayer {
  constructor({ gameId, engine, ports, seed = 1234, onEvent = null, name = 'Лай' }) {
    this.gameId = gameId;
    this.eng = engine;
    this.ports = ports;
    this.name = name;
    this.onEvent = onEvent;
    this.rng = mulberry32(seed);
    this.career = { games: 0, best: 0, totalScore: 0, lessons: [], caution: 0 };
    this.busy = false;
    this.portIdx = 0;
    this.lastLatency = 0;
    this.lastDecision = null;
    this.events = [];
    this.state = engine.newState(this.rng);
    this.episodes = 0;
    this.sumScore = 0;
    this.overrides = 0;
    this.seatbeltMoves = 0;
    this.layaMoves = 0;
    // Сколько ходов пришлось сделать по правилам, потому что ни один Лай не ответил.
    this.modelOfflineMoves = 0;
  }

  /**
   * Лучший вариант с точки зрения правил: берём метку с наивысшим приоритетом
   * безопасности для этой игры. Нужен там, где модель недоступна, — арена не
   * должна замирать из-за незапущенного флота.
   */
  pickSafeOption(options) {
    if (!options?.length) return null;
    const order = SAFE_ORDER[this.gameId] || [];
    let best = null;
    let bestRank = Number.MAX_SAFE_INTEGER;
    for (const o of options) {
      const i = order.indexOf(o.label);
      const rank = i < 0 ? order.length : i;
      if (rank < bestRank) { bestRank = rank; best = o; }
    }
    return best || options[0];
  }

  get score() { return this.eng.score(this.state); }

  /** Уроки влияют на осторожность: чем больше смертей такого типа, тем больше требуемый запас места. */
  updateCaution() {
    const key = this.gameId === 'snake' ? 'body' : (this.gameId === 'mines' ? 'mine' : 'over');
    this.career.caution = this.career.lessons.filter((l) => l.death === key).length;
  }

  /** Память карьеры (сохраняется сервером). */
  careerSnapshot() {
    return {
      games: this.career.games,
      best: this.career.best,
      totalScore: this.career.totalScore,
      caution: this.career.caution,
      lessons: this.career.lessons.slice(0, 8),
    };
  }

  restoreCareer(saved) {
    if (!saved) return;
    this.career.games = saved.games || 0;
    this.career.best = saved.best || 0;
    this.career.totalScore = saved.totalScore || 0;
    this.career.lessons = Array.isArray(saved.lessons) ? saved.lessons.slice(0, 8) : [];
    this.episodes = this.career.games;
    this.sumScore = this.career.totalScore;
    this.updateCaution();
  }

  finishEpisode() {
    const score = this.eng.score(this.state);
    if (score > this.career.best) this.career.best = score;
    this.career.games++;
    this.career.totalScore += score;
    this.episodes++;
    this.sumScore += score;

    const lesson = this.eng.makeLesson(this.state, this.career.games, score);
    this.career.lessons.unshift(lesson);
    if (this.career.lessons.length > 12) this.career.lessons.pop();
    this.updateCaution();

    this.emit({
      kind: 'lesson',
      label: `урок #${lesson.n} · ${lesson.death}`,
      thought: lesson.note,
      score,
      caution: this.career.caution,
    });

    this.state = this.eng.newState(this.rng);
  }

  emit(ev) {
    const row = { t: clock(), game: this.gameId, ...ev };
    this.events.unshift(row);
    if (this.events.length > 12) this.events.pop();
    if (this.onEvent) this.onEvent(row);
  }

  /** Один ход: предохранитель → (при неочевидности) вопрос Лаям → предохранитель. */
  async turn() {
    if (this.busy || !this.ports.length) return;
    this.busy = true;
    try {
      if (!this.state || this.eng.isOver(this.state)) this.finishEpisode();
      const eng = this.eng;
      const caution = this.career.caution;

      // 1. Предохранитель: очевидный ход — модель не нужна
      const obvious = eng.obviousMove ? eng.obviousMove(this.state, caution, this.rng) : null;
      if (obvious) {
        this.seatbeltMoves++;
        const done = this.apply(obvious.action, {
          kind: 'seatbelt',
          label: obvious.label || 'ход по правилам',
          thought: obvious.thought || 'предохранитель: ход очевиден',
          mark: obvious.mark || 'OK',
        });
        if (done) this.finishEpisode();
        return;
      }

      // 2. Неочевидно: спрашиваем Лая (несколько экземпляров голосуют)
      const sit = eng.layaSituation(this.state, this.career, caution);
      if (!sit || !sit.options?.length) { this.finishEpisode(); return; }

      const t0 = Date.now();
      const voters = Math.min(2, this.ports.length);
      const answers = await Promise.all(Array.from({ length: voters }, (_, k) => {
        const port = this.ports[(this.portIdx + k) % this.ports.length];
        return decide(port, { state: sit.state, questions: sit.questions })
          .then((r) => ({ port, answers: r.answers }))
          .catch(() => null);
      }));
      this.portIdx = (this.portIdx + voters) % this.ports.length;
      this.lastLatency = Date.now() - t0;

      const good = answers.filter(Boolean);
      let wantedOption = null;
      let ranked = [];
      let probs = {};          // усреднённые вероятности вариантов — нужны ветке змейки ниже
      let offline = false;

      if (good.length) {
        // вероятности по вариантам — усредняем мнения Лаев
        const critKeys = Object.keys(sit.questions.move.criteria);
        for (const { answers: a } of good) {
          const mv = a.move;
          if (!mv?.probabilities) continue;
          critKeys.forEach((key, i) => {
            const p = mv.probabilities[i];
            if (p) probs[key] = (probs[key] ?? 0) + p.value / good.length;
          });
        }
        ranked = critKeys
          .map((key) => ({ key, p: probs[key] ?? 0 }))
          .sort((a, b) => b.p - a.p);
        const wantedLabel = ranked[0]?.key;
        wantedOption = sit.options.find((o) => o.label === wantedLabel) || sit.options[0];
      } else {
        // Все экземпляры Лая молчат (обычно закрыты порты 8101..8105). Раньше ход
        // просто не делался и арена замирала навсегда: в интерфейсе «Лаи offline»,
        // а игра стоит. Теперь правила выбирают самый безопасный вариант сами, а
        // событие в ленте честно говорит, что модель не отвечала.
        offline = true;
        this.modelOfflineMoves++;
        wantedOption = this.pickSafeOption(sit.options);
        if (!wantedOption) { this.finishEpisode(); return; }
      }

      // 3. Предохранитель проверяет выбор модели
      let action;
      let overridden = false;
      let reason = null;
      let mark = wantedOption.mark;

      if (this.gameId === 'mines') {
        const res = eng.resolveMove(this.state, wantedOption.idx);
        action = { type: 'open', idx: res.idx };
        overridden = res.overridden;
        reason = res.reason;
        mark = res.mark;
      } else if (this.gameId === 'tetris') {
        const res = eng.resolvePlacement(this.state, wantedOption.place, sit.all);
        action = { type: 'place', place: { r: res.place.r, x: res.place.x, cleared: res.place.stat.cleared, board: res.place.board } };
        overridden = res.overridden;
        reason = res.reason;
        mark = res.mark;
      } else {
        const dir = wantedOption.dir ?? this.state.dir;
        const res = eng.resolveMove(this.state, dir, probs, caution);
        action = res.action;
        overridden = res.overridden;
        reason = res.reason;
        mark = res.mark;
      }

      this.layaMoves++;
      if (overridden) this.overrides++;

      const votes = ranked.map((r) => `${r.key} ${(r.p * 100).toFixed(0)}%`).join(' · ');
      const ports = this.ports.join(', ');
      let thought;
      if (offline) {
        thought = `Лаи не ответили (порты ${ports}) → правила выбрали «${wantedOption.label}» (${mark})`
          + (overridden ? `; предохранитель заменил: ${reason}` : '')
          + ' — проверьте, что экземпляры Laya запущены';
      } else if (overridden) {
        thought = `Лаи: ${votes} → выбрано «${wantedOption.label}» (${wantedOption.mark}); предохранитель заменил: ${reason}`;
      } else {
        thought = `Лаи: ${votes} → «${wantedOption.label}» (${wantedOption.mark}), предохранитель согласен`;
      }

      const done = this.apply(action, {
        kind: offline ? 'offline' : (overridden ? 'override' : 'laya'),
        label: wantedOption.label,
        thought,
        mark,
        latency: this.lastLatency,
      });
      if (done) this.finishEpisode();
    } catch (e) {
      this.emit({ kind: 'error', label: 'ошибка хода', thought: String(e.message) });
    } finally {
      this.busy = false;
    }
  }

  apply(action, info) {
    const r = this.eng.step(this.state, action, this.rng);
    this.lastDecision = { ...info, score: this.eng.score(this.state), reward: r.reward };
    this.emit({
      ...info,
      score: this.eng.score(this.state),
      caution: this.career.caution,
      games: this.career.games,
    });
    return !!r.done;
  }
}

/* Простой ГПСЧ, чтобы партии были воспроизводимы. */
function mulberry32(seed) {
  let a = seed >>> 0;
  return function () {
    a |= 0; a = (a + 0x6D2B79F5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}
