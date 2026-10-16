// trainer.mjs — серверный тренер: эволюция живёт в процессе Node, а не в браузере.
//
// Зачем: пока обучение шло в Web Worker, закрытие или обновление вкладки уничтожало
// популяции, а «мозг» лежал в localStorage браузера. Теперь тренер работает на сервере,
// периодически сохраняет состояние (лучшие сети, историю, счётчики, рекорды Лая-игроков)
// в файл на диске и продолжает обучение с него после перезапуска.
import fs from 'node:fs';
import path from 'node:path';
import { Population, Net } from '../src/brain.js';
import { SnakeEngine, MinesEngine, TetrisEngine } from '../src/games.js';
import { LayaPlayer } from './laya-player.mjs';

const GAMES = [
  { id: 'snake', make: () => new SnakeEngine() },
  { id: 'mines', make: () => new MinesEngine() },
  { id: 'tetris', make: () => new TetrisEngine() },
];

// Тройка Лаев на каждую игру: их ответы видно в лентах решений.
const LAYA_PORTS = [8095, 8101, 8102, 8103, 8104, 8105];
const PLAYER_PORTS = {
  snake: [LAYA_PORTS[0], LAYA_PORTS[1], LAYA_PORTS[2]],
  mines: [LAYA_PORTS[3], LAYA_PORTS[4], LAYA_PORTS[5]],
  tetris: [LAYA_PORTS[0], LAYA_PORTS[2], LAYA_PORTS[4]],
};

const clock = () => new Date().toLocaleTimeString('ru-RU');

export class Trainer {
  constructor({ stateFile, autosaveMs = 15000, budgetMs = 10 } = {}) {
    this.stateFile = stateFile;
    this.autosaveMs = autosaveMs;
    this.budgetMs = budgetMs;
    this.paused = false;
    this.focusMode = 'best';

    this.engines = {};
    this.pops = {};
    this.players = {};
    this.playerLog = { snake: [], mines: [], tetris: [] };
    this.totalGenerations = 0;
    this.pendingLog = [];
    this.pendingThoughts = { snake: [], mines: [], tetris: [] };
    this.lastThoughtAt = {};
    this.lastGenLog = {};
    this.skippedGens = 0;
    this.startedAt = Date.now();
    this.lastSavedAt = null;
    this.restoredFrom = null;
    this.stateProblem = null;
    this.saves = 0;

    this._restore();
    this._loop();
    this._autosaveTimer = setInterval(() => this.saveState(), this.autosaveMs);
    if (this._autosaveTimer.unref) this._autosaveTimer.unref();
  }

  /* ------------------------------ создание популяций ------------------------------ */
  _create(seedNets = {}, seedScores = {}) {
    const seeds = { snake: 20260928, mines: 777123, tetris: 424242 };
    this.engines = {};
    this.pops = {};
    for (const g of GAMES) {
      this.engines[g.id] = g.make();
      const pop = new Population(this.engines[g.id], {
        seed: seeds[g.id],
        seedNet: seedNets[g.id] ? Net.deserialize(seedNets[g.id]) : null,
        seedScore: seedScores[g.id] || 0,
      });
      this._attach(pop, g);
      this.pops[g.id] = pop;
      this.lastThoughtAt[g.id] = 0;
      this.lastGenLog[g.id] = 0;
    }
    this.pendingThoughts = { snake: [], mines: [], tetris: [] };
  }

  /* ------------------------------ игроки-Лаи (серверные) ------------------------------ */
  _createPlayers(savedCareers = {}) {
    this.players = {};
    for (const g of GAMES) {
      const p = new LayaPlayer({
        gameId: g.id,
        engine: this.engines[g.id],
        ports: PLAYER_PORTS[g.id] || LAYA_PORTS,
        seed: 1000 + g.id.length * 37,
        onEvent: (row) => {
          const log = this.playerLog[g.id];
          if (!log) return;
          log.unshift(row);
          if (log.length > 10) log.pop();
        },
      });
      p.restoreCareer(savedCareers[g.id]);
      this.players[g.id] = p;
    }
  }

  /** Ходы игроков: асинхронно, не блокируя обучение. */
  _playersTick() {
    if (this.paused) return;
    for (const g of GAMES) {
      const p = this.players[g.id];
      if (!p || typeof p.turn !== 'function' || p.busy) continue;
      p.turn().catch(() => {});
    }
  }

  _attach(pop, g) {
    const eng = pop.game;

    pop.onDecision = (d, m, p, r) => {
      const now = performance.now();
      if (now - (this.lastThoughtAt[g.id] || 0) < 110) return;
      this.lastThoughtAt[g.id] = now;
      const arr = this.pendingThoughts[g.id];
      if (!arr) return;
      arr.push({
        t: clock(),
        thought: d.thought,
        label: d.label || '—',
        reward: r ? r.reward : 0,
        fitness: m.fitness,
        score: eng.score(m.state),
        kind: d.kind || 'net',
        risk: d.risk,
        source: 'learn',
      });
      if (arr.length > 8) arr.splice(0, arr.length - 8);
    };

    pop.onGeneration = (info) => {
      this.totalGenerations++;
      const now = performance.now();
      const quiet = now - (this.lastGenLog[g.id] || 0) < 3000;
      if (quiet) { this.skippedGens++; return; }
      this.lastGenLog[g.id] = now;
      this.pendingLog.push({
        t: clock(),
        game: g.id,
        title: eng.title,
        scoreName: g.id === 'tetris' ? 'очки' : (g.id === 'mines' ? 'открыто' : 'длина'),
        gen: info.gen,
        nextGen: info.nextGen,
        best: info.best.score,
        avgF: info.avgF,
        mutationRate: info.mutationRate,
        skipped: this.skippedGens,
      });
      this.skippedGens = 0;
      if (this.pendingLog.length > 60) this.pendingLog.splice(0, this.pendingLog.length - 60);
    };
    void pop;
  }

  /* ------------------------------ состояние на диск ------------------------------ */
  saveState() {
    const games = {};
    for (const g of GAMES) {
      const pop = this.pops[g.id];
      if (!pop) continue;
      const net = pop.bestEverNet || pop.focus?.net;
      if (!net) continue;
      games[g.id] = {
        best: pop.bestScore,
        generation: pop.generation,
        episodes: pop.episodes,
        history: pop.history.slice(-130),
        mutationRate: pop.mutationRate,
        mutationAmp: pop.mutationAmp,
        bestEverFitness: Number.isFinite(pop.bestEverFitness) ? pop.bestEverFitness : null,
        net: net.serialize(),
      };
    }
    const payload = {
      version: 3,
      savedAt: new Date().toISOString(),
      totalGenerations: this.totalGenerations,
      players: Object.fromEntries(GAMES.map((g) => [g.id, this.players[g.id] ? this.players[g.id].careerSnapshot() : null])),
      games,
    };
    try {
      fs.mkdirSync(path.dirname(this.stateFile), { recursive: true });
      // Резервная копия предыдущего состояния: если следующий запуск не сможет
      // прочитать основной файл, прогресс останется в .bak, а не пропадёт.
      if (fs.existsSync(this.stateFile)) {
        try { fs.copyFileSync(this.stateFile, `${this.stateFile}.bak`); } catch { /* копия не критична */ }
      }
      const tmp = `${this.stateFile}.tmp`;
      fs.writeFileSync(tmp, JSON.stringify(payload), 'utf8');
      fs.renameSync(tmp, this.stateFile);
      this.lastSavedAt = payload.savedAt;
      this.saves++;
    } catch (e) {
      console.error('[тренер] не удалось сохранить состояние:', e.message);
    }
    return payload;
  }

  /**
   * Чтение состояния: сначала основной файл, затем резервная копия .bak.
   *
   * Раньше любая ошибка чтения молча превращалась в «состояния нет»: сервис
   * начинал обучение с нуля и первым же автосохранением затирал рабочий файл —
   * со стороны это выглядело как «прогресс теряется при каждом запуске».
   * Теперь причина попадает в лог и в снапшот, а негодный файл не удаляется,
   * а отодвигается в сторону с отметкой времени.
   */
  _readState() {
    const candidates = [
      { file: this.stateFile, main: true },
      { file: `${this.stateFile}.bak`, main: false },
    ];
    const problems = [];

    for (const { file, main } of candidates) {
      let raw;
      try {
        raw = fs.readFileSync(file, 'utf8');
      } catch (e) {
        problems.push(`${path.basename(file)}: нет доступа (${e.code || e.message})`);
        continue;
      }
      let parsed;
      try {
        parsed = JSON.parse(raw);
      } catch (e) {
        problems.push(`${path.basename(file)}: повреждён JSON (${e.message})`);
        continue;
      }
      if (!parsed || typeof parsed !== 'object' || !parsed.games) {
        problems.push(`${path.basename(file)}: нет блока games`);
        continue;
      }
      if (!main) console.error(`[тренер] основной файл состояния не годен — продолжаю из ${path.basename(file)}`);
      return { saved: parsed, problem: null };
    }

    // Ни один файл не прочитался: испорченные файлы отодвигаем, чтобы они не
    // были перезаписаны первым же автосохранением, и сообщаем о причине.
    for (const { file } of candidates) {
      try {
        if (fs.existsSync(file)) fs.renameSync(file, `${file}.corrupt-${Date.now()}`);
      } catch { /* ничего страшного: файл просто останется на месте */ }
    }
    return { saved: null, problem: problems.join('; ') || 'файла состояния нет' };
  }

  _restore() {
    const { saved, problem } = this._readState();
    this.stateProblem = problem;
    if (problem) console.error(`[тренер] состояние не восстановлено: ${problem}`);

    const nets = {}, scores = {};
    if (saved?.games) {
      for (const g of GAMES) {
        const it = saved.games[g.id];
        if (it?.net) { nets[g.id] = it.net; scores[g.id] = it.best || 0; }
      }
    }
    this._create(nets, scores);
    if (saved?.games) {
      for (const g of GAMES) {
        const it = saved.games[g.id];
        const pop = this.pops[g.id];
        if (!it || !pop) continue;
        if (Array.isArray(it.history) && it.history.length) pop.history = it.history;
        pop.generation = it.generation || pop.generation;
        pop.episodes = it.episodes || 0;
        pop.bestScore = it.best || 0;
        pop.bestEverScore = it.best || 0;
        if (it.mutationRate) pop.mutationRate = it.mutationRate;
        if (it.mutationAmp) pop.mutationAmp = it.mutationAmp;
        if (it.bestEverFitness != null) pop.bestEverFitness = it.bestEverFitness;
      }
      this.totalGenerations = saved.totalGenerations || 0;
      this.restoredFrom = saved.savedAt || null;
      console.log(`[тренер] состояние восстановлено из ${this.stateFile} (сохранено ${this.restoredFrom})`);
      this._createPlayers(saved.players || {});
    } else {
      console.log('[тренер] сохранённого состояния нет — обучение начинается с нуля');
      this._createPlayers({});
    }
    const lessons = GAMES.reduce((n, g) => n + (this.players[g.id]?.career.lessons.length || 0), 0);
    if (lessons) console.log(`[тренер] уроков в карьере Лаев: ${lessons} — они подаются модели в следующих партиях`);
  }

  /* ------------------------------ команды ------------------------------ */
  setPaused(v) { this.paused = !!v; return this.paused; }

  setBudget(ms) { this.budgetMs = Math.max(1, Math.min(200, Number(ms) || 10)); return this.budgetMs; }

  setFocus(mode) {
    this.focusMode = mode === 'random' ? 'random' : 'best';
    for (const g of GAMES) {
      const pop = this.pops[g.id];
      if (!pop) continue;
      pop.focusMode = this.focusMode;
      pop.focusIndex = this.focusMode === 'best' ? 0 : Math.floor(Math.random() * pop.size);
    }
    return this.focusMode;
  }

  reset() {
    this.totalGenerations = 0;
    this._create();
    this._createPlayers({});
    for (const g of GAMES) this.playerLog[g.id] = [];
    this.saveState();
    return true;
  }

  /** Продолжить обучение с переданных сетей (кнопка «Загрузить» или внешний файл). */
  loadNets(nets = {}, scores = {}) {
    this._create(nets, scores);
    this.saveState();
    return true;
  }

  /** Совместимость: внешние рекорды лишь подтягивают карьеру, не подменяя игроков. */
  mergePlayers(players) {
    if (!players) return;
    for (const g of GAMES) {
      const incoming = players[g.id];
      const p = this.players[g.id];
      if (!incoming || !p || typeof p.career !== 'object') continue;
      if ((incoming.bestScore || 0) > p.career.best) p.career.best = incoming.bestScore;
      if ((incoming.episodes || 0) > p.episodes) p.episodes = incoming.episodes;
      if ((incoming.scoreSum || 0) > p.sumScore) p.sumScore = incoming.scoreSum;
    }
  }

  /* ------------------------------ цикл ------------------------------ */
  _loop() {
    if (!this.paused) {
      const per = Math.max(0.8, this.budgetMs / GAMES.length);
      for (const g of GAMES) {
        const pop = this.pops[g.id];
        if (!pop) continue;
        const deadline = performance.now() + per;
        let guard = 0;
        while (performance.now() < deadline && guard++ < 20000) pop.tick();
      }
      // ходы Лаев — асинхронно, своим темпом (модель отвечает ~100–200 мс)
      const now = Date.now();
      if (now - (this._lastPlayerTick || 0) > 40) {
        this._lastPlayerTick = now;
        this._playersTick();
      }
    }
    this._loopTimer = setTimeout(() => this._loop(), 2);   // короткая пауза: сервер остаётся отзывчивым
    if (this._loopTimer.unref) this._loopTimer.unref();
  }

  /* ------------------------------ снапшот для страницы ------------------------------ */
  snapshot() {
    const games = {};
    for (const g of GAMES) {
      const pop = this.pops[g.id];
      const eng = this.engines[g.id];
      const player = this.players[g.id];
      if (!pop) continue;
      const f = pop.focus;
      games[g.id] = {
        generation: pop.generation,
        size: pop.size,
        focusIndex: pop.focusIndex,
        episodes: pop.episodes,
        bestScore: pop.bestScore,
        avgScore: pop.avgScore,
        bestFitness: pop.bestFitness,
        avgFitness: pop.avgFitness,
        mutationRate: pop.mutationRate,
        mutationAmp: pop.mutationAmp,
        stepsPerSec: pop.recordSteps,
        history: pop.history.slice(-30).map((h) => [h.gen, h.best, h.avg, h.bestFit ?? 0, h.avgFit ?? 0]),   // компактно: массив вместо объектов
        focusView: f ? eng.view(f.state) : null,
        focus: f ? { steps: f.steps, fitness: f.fitness, done: f.done, score: eng.score(f.state) } : null,
        // игрок-Лай: арена, карьера с уроками и последнее решение
        laya: player ? {
          view: eng.view(player.state),
          score: player.score,
          best: player.career.best,
          games: player.career.games,
          caution: player.career.caution,
          avgScore: player.episodes ? +(player.sumScore / player.episodes).toFixed(1) : 0,
          lessons: player.career.lessons.slice(0, 3),
          lastDecision: player.lastDecision,
          seatbeltMoves: player.seatbeltMoves,
          layaMoves: player.layaMoves,
          overrides: player.overrides,
          modelOfflineMoves: player.modelOfflineMoves || 0,
          events: this.playerLog[g.id].slice(0, 4),
        } : null,
      };
    }
    return {
      server: true,
      paused: this.paused,
      budgetMs: this.budgetMs,
      focusMode: this.focusMode,
      totalGenerations: this.totalGenerations,
      uptimeSec: Math.round((Date.now() - this.startedAt) / 1000),
      lastSavedAt: this.lastSavedAt,
      restoredFrom: this.restoredFrom,
      stateProblem: this.stateProblem,
      saves: this.saves,
      players: Object.fromEntries(GAMES.map((g) => [g.id, this.players[g.id] ? this.players[g.id].careerSnapshot() : null])),
      games,
      log: this.pendingLog.splice(0, this.pendingLog.length),
      thoughts: {
        snake: this.pendingThoughts.snake.splice(0, this.pendingThoughts.snake.length),
        mines: this.pendingThoughts.mines.splice(0, this.pendingThoughts.mines.length),
        tetris: this.pendingThoughts.tetris.splice(0, this.pendingThoughts.tetris.length),
      },
    };
  }

  /** Мозг в формате, пригодном для скачивания файлом. */
  exportBrain() {
    const payload = { agent: 'Лай-ученик', ts: Date.now(), games: {} };
    for (const g of GAMES) {
      const pop = this.pops[g.id];
      if (!pop) continue;
      const net = pop.bestEverNet || pop.focus?.net;
      payload.games[g.id] = {
        best: pop.bestScore,
        generation: pop.generation,
        episodes: pop.episodes,
        history: pop.history,
        net: net ? net.serialize() : null,
      };
    }
    return payload;
  }

  stop() {
    clearTimeout(this._loopTimer);
    clearInterval(this._autosaveTimer);
    this.saveState();
  }
}
