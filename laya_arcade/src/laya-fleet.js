// laya-fleet.js — флот экземпляров Laya (Go-сервис, модель на RTX 3060).
// Каждый экземпляр — отдельный процесс со своими весами в VRAM; страница
// обращается к ним через прокси своего же сервера (/laya/<порт>/...).

export const LAYA_PORTS = [8095, 8101, 8102, 8103, 8104, 8105];

/** Роли экспертов: один и тот же Лай отвечает на несколько типизированных вопросов. */
export const EXPERT_ROLES = {
  snake: {
    safe: { type: 'noul', instructions: 'Погибнет ли змейка, если сделает этот ход (стена или своё тело)?' },
    goal: { type: 'noul', instructions: 'Приблизит ли этот ход змейку к еде?' },
    room: { type: 'noul', instructions: 'Останется ли у змейки достаточно свободного места для манёвра после этого хода?' },
  },
  mines: {
    mine: { type: 'noul', instructions: 'Есть ли мина в этой клетке сапёра?' },
    logic: { type: 'noul', instructions: 'Согласуется ли открытие этой клетки с числами на соседних открытых клетках?' },
    progress: { type: 'noul', instructions: 'Откроет ли эта клетка много новых безопасных клеток?' },
  },
  tetris: {
    height: { type: 'noul', instructions: 'Останется ли стакан тетриса безопасным по высоте после этого размещения фигуры?' },
    holes: { type: 'noul', instructions: 'Свободно ли размещение от появления дыр под фигурой?' },
    lines: { type: 'noul', instructions: 'Принесёт ли это размещение заполненные линии?' },
  },
};

/** Вес каждой роли при сведении мнений в одну оценку варианта. */
export const ROLE_WEIGHTS = {
  snake: { safe: -1.0, goal: 0.9, room: 0.5 },
  mines: { mine: -1.0, logic: 0.5, progress: 0.5 },
  tetris: { height: 0.7, holes: 0.8, lines: 1.0 },
};

export class LayaFleet {
  constructor(ports = LAYA_PORTS) {
    this.agents = ports.map((port, i) => ({
      id: i + 1,
      port,
      status: 'unknown',      // unknown | online | offline
      device: '',
      requests: 0,
      latency: 0,
      answered: 0,
      errors: 0,
      lastAnswerAt: 0,
      lastVerdict: '',
      busy: false,
    }));
    this.onUpdate = null;
    this.totalVerdicts = 0;
    this.totalLatency = 0;
  }

  get online() { return this.agents.filter((a) => a.status === 'online'); }

  /** Тройка Лаев, обслуживающая игру: у каждой игры свои три эксперта. */
  triple(gameId) {
    const idx = ['snake', 'mines', 'tetris'].indexOf(gameId);
    const on = this.online;
    if (!on.length) return [];
    const trio = [];
    for (let i = 0; i < 3; i++) trio.push(on[(idx * 3 + i) % on.length]);
    return trio;
  }

  async probe(agent) {
    const t0 = performance.now();
    try {
      const r = await fetch(`/laya/${agent.port}/info`, { cache: 'no-store' });
      if (!r.ok) throw new Error('HTTP ' + r.status);
      const j = await r.json();
      agent.status = 'online';
      agent.device = j.device || '?';
      agent.requests = j.stats?.requests ?? 0;
      agent.latency = Math.round(performance.now() - t0);
    } catch (e) {
      agent.status = 'offline';
      agent.errors++;
    }
  }

  async probeAll() {
    await Promise.all(this.agents.map((a) => this.probe(a)));
    if (this.onUpdate) this.onUpdate(this);
  }

  /** Один Лай оценивает один вариант: описание ситуации + роли-вопросы. */
  async judge(agent, stateText, questions) {
    const t0 = performance.now();
    agent.busy = true;
    try {
      const r = await fetch(`/laya/${agent.port}/decide`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ state: stateText, questions }),
      });
      if (!r.ok) throw new Error('HTTP ' + r.status);
      const j = await r.json();
      const ms = performance.now() - t0;
      agent.latency = Math.round(ms);
      agent.answered++;
      agent.requests = (agent.requests || 0) + 1;
      agent.lastAnswerAt = Date.now();
      this.totalVerdicts++;
      this.totalLatency += ms;
      return j.answers;
    } finally {
      agent.busy = false;
    }
  }

  /**
   * Режим «closer»: для каждого безопасного хода спрашиваем Лаев бинарно —
   * «уменьшит ли этот ход расстояние до цели?». Вероятности усредняются по
   * нескольким Лаям (это заметно точнее, чем один вопрос-выбор из трёх).
   * Выбирается ход с наибольшей средней вероятностью; при равенстве — ближайший.
   */
  async verdictCloser(gameId, situation) {
    const trio = this.triple(gameId);
    if (!trio.length || !situation?.options?.length) return null;

    const perOption = situation.perOption ?? 1;
    const jobs = [];
    situation.options.forEach((opt, i) => {
      for (let k = 0; k < perOption; k++) {
        const agent = trio[(i + k) % trio.length];
        jobs.push(
          this.judge(agent, opt.text, situation.questions)
            .then((answers) => ({ i, agent, p: answers.closer?.noul ?? 0.5 }))
            .catch(() => null)
        );
      }
    });

    const got = (await Promise.all(jobs)).filter(Boolean);
    if (!got.length) return null;

    const rows = situation.options.map((o, i) => ({
      index: i, label: o.label, action: o.action, dist: o.dist ?? 0, order: i,
      sum: 0, n: 0, agents: [],
    }));
    for (const g of got) {
      const r = rows[g.i];
      if (!r) continue;
      r.sum += g.p; r.n++; r.agents.push(g.agent.id);
    }
    rows.forEach((r) => { r.score = r.n ? r.sum / r.n : 0; });

    rows.sort((a, b) => b.score - a.score || a.dist - b.dist || a.order - b.order);

    return {
      t: Date.now(),
      key: situation.key,
      mode: 'closer',
      best: { ...rows[0], score: +rows[0].score.toFixed(3) },
      all: rows.map((r) => ({ ...r, score: +r.score.toFixed(3) })),
      agents: [...new Set(rows.flatMap((r) => r.agents))],
      votes: perOption,
    };
  }

  /**
   * Режим «choice»: Лай отвечает не баллами на бинарные вопросы, а прямым
   * выбором одного из ходов. Все Лаи тройки голосуют, вероятности по вариантам
   * суммируются, смертельные варианты (если Лай их назвал) исключаются.
   */
  async verdictChoice(gameId, situation) {
    const trio = this.triple(gameId);
    if (!trio.length || !situation?.options?.length) return null;

    const got = (await Promise.all(trio.map(async (agent) => {
      try {
        const answers = await this.judge(agent, situation.state, situation.questions);
        return { agent, answers };
      } catch (e) {
        agent.errors++;
        return null;
      }
    }))).filter(Boolean);
    if (!got.length) return null;

    const critKeys = Object.keys(situation.questions.move.criteria);
    const rows = situation.options.map((o, i) => ({
      index: i, label: o.label, action: o.action, score: 0, votes: 0, lethalVotes: 0, probs: [],
    }));

    for (const { agent, answers } of got) {
      const mv = answers.move;
      if (mv?.probabilities) {
        mv.probabilities.forEach((p, i) => {
          if (rows[i]) { rows[i].score += p.value; rows[i].probs.push(p.value); }
        });
      }
      if (mv?.choice) {
        const i = critKeys.indexOf(mv.choice);
        if (rows[i]) rows[i].votes++;
      }
      const le = answers.lethal;
      if (le?.choice && le.choice !== 'none') {
        const i = critKeys.indexOf(le.choice);
        if (rows[i]) rows[i].lethalVotes++;
      }
    }

    // смертельные варианты отбрасываем, если после этого остаётся хоть один ход
    const votesNeeded = Math.ceil(got.length / 2);
    const alive = rows.filter((r) => r.lethalVotes < votesNeeded);
    const pool = alive.length ? alive : rows;
    pool.sort((a, b) => b.score - a.score);

    return {
      t: Date.now(),
      key: situation.key,
      mode: 'choice',
      best: pool[0],
      all: pool.map((r) => ({ ...r, score: +r.score.toFixed(3), probs: r.probs.map((p) => +p.toFixed(3)) })),
      agents: got.map((g) => g.agent.id),
      votes: got.length,
      lethal: rows.filter((r) => r.lethalVotes >= votesNeeded).map((r) => r.label),
    };
  }

  /**
   * Вердикт по ситуации: каждый вариант оценивает свой Лай из тройки,
   * затем мнения сводятся в единый балл и выбирается лучший вариант.
   */
  async verdict(gameId, situation) {
    if (situation?.mode === 'choice') return this.verdictChoice(gameId, situation);
    if (situation?.mode === 'closer') return this.verdictCloser(gameId, situation);

    const trio = this.triple(gameId);
    if (!trio.length || !situation?.options?.length) return null;

    const weights = ROLE_WEIGHTS[gameId] || {};
    const roles = situation.questions;

    const judged = await Promise.all(situation.options.map(async (opt, i) => {
      const agent = trio[i % trio.length];
      try {
        const answers = await this.judge(agent, opt.text, roles);
        let score = 0, detail = [];
        for (const [role, w] of Object.entries(weights)) {
          const a = answers[role];
          if (!a) continue;
          const p = a.type === 'noul' ? a.noul : (a.answer_confidence ?? 0.5);
          score += w * p;
          detail.push({ role, p, label: a.type === 'noul' ? (a.noul >= 0.5 ? 'да' : 'нет') : '' });
        }
        return { index: i, label: opt.label, action: opt.action, score, detail, agentId: agent.id, port: agent.port };
      } catch (e) {
        agent.errors++;
        return null;
      }
    }));

    const ok = judged.filter(Boolean);
    if (!ok.length) return null;
    ok.sort((a, b) => b.score - a.score);
    return {
      t: Date.now(),
      key: situation.key,
      best: ok[0],
      all: ok,
      agents: ok.map((o) => o.agentId),
    };
  }

  stats() {
    const avg = this.totalVerdicts ? this.totalLatency / this.totalVerdicts : 0;
    return {
      online: this.online.length,
      total: this.agents.length,
      verdicts: this.totalVerdicts,
      avgLatency: avg,
    };
  }
}
