// app.js — интерфейс аркады.
//  • «Ученик» обучается в Web Worker (поток обучения) — независимо от активности вкладки.
//  • «Лаи» — флот экземпляров модели Laya на RTX 3060 — играют на аренах (main-поток, fetch).
import { LayaFleet, EXPERT_ROLES, LAYA_PORTS } from './laya-fleet.js';
import { SnakeEngine, MinesEngine, TetrisEngine } from './games.js';
import { mulberry32 } from './brain.js';

const GAMES = [
  { id: 'snake', make: () => new SnakeEngine(), scoreName: 'длина', color: '#4dd6ff' },
  { id: 'mines', make: () => new MinesEngine(), scoreName: 'открыто', color: '#3fd28a' },
  { id: 'tetris', make: () => new TetrisEngine(), scoreName: 'очки', color: '#ffd166' },
];

const state = {
  paused: false,
  budgetMs: 10,
  focusMode: 'best',
  engines: {},
  thoughts: {},
  log: [],
  snapshot: {},
  totalGenerations: 0,
  view: { snake: 'laya', mines: 'laya', tetris: 'laya' },   // 'laya' | 'learn'
  fleet: null,
  players: {},
  seenEvents: {},
  careers: {},
  careerSig: {},
  serverOnline: false,
  serverError: '',
  serverMeta: {},
  startedAt: performance.now(),
  errors: [],
  lastRecord: {},
};

/* ------------------------------ утилиты ------------------------------ */
const $ = (sel) => document.querySelector(sel);
const el = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
};
const clock = () => new Date().toLocaleTimeString('ru-RU');
const fmt = (n, d = 1) => (Number.isFinite(n) ? n.toFixed(d) : '—');
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/* ------------------------------ журнал ------------------------------ */
function journalNode(row) {
  const d = el('div', `jrow jrow-${row.kind}`);
  d.appendChild(el('span', 'jtime', row.t));
  d.appendChild(el('span', 'jtext', row.text));
  return d;
}

function pushLog(text, kind = 'info') {
  state.log.unshift({ t: clock(), text, kind });
  if (state.log.length > 300) state.log.pop();
  // одна новая строка сверху вместо пересборки всего журнала
  const box = $('#journal');
  if (!box) return;
  box.insertBefore(journalNode(state.log[0]), box.firstChild);
  while (box.childElementCount > 120) box.removeChild(box.lastElementChild);
}

function renderJournal() {
  const box = $('#journal');
  if (!box) return;
  box.innerHTML = '';
  for (const row of state.log.slice(0, 120)) box.appendChild(journalNode(row));
}

/* ------------------------------ лента действий ------------------------------ */
function thoughtNode(row) {
  const line = el('div', `th th-${row.kind}`);
  const head = el('div', 'th-head');
  head.appendChild(el('span', 'th-time', row.t));
  head.appendChild(el('span', 'th-label', row.label || '—'));
  if (row.latency) head.appendChild(el('span', 'th-lat', `${row.latency} мс`));
  const rw = row.reward >= 0 ? `+${fmt(row.reward, 2)}` : fmt(row.reward, 2);
  head.appendChild(el('span', `th-rw ${row.reward >= 0 ? 'rw-pos' : 'rw-neg'}`, rw));
  line.appendChild(head);
  line.appendChild(el('div', 'th-text', row.thought));
  line.appendChild(el('div', 'th-meta',
    `фитнес ${fmt(row.fitness, 1)} · счёт ${row.score}` +
    (row.via?.length ? ` · отвечали Лаи ${row.via.map((v) => '#' + v).join(', ')}` : '') +
    (row.risk != null ? ` · риск ${(row.risk * 100).toFixed(1)}%` : '')));
  return line;
}

function pushThought(gameId, row) {
  const arr = state.thoughts[gameId];
  if (!arr) return;
  arr.unshift(row);
  if (arr.length > 16) arr.pop();
  // быстрый путь: одна новая запись сверху, без пересборки всей ленты
  const box = $(`#th-${gameId}`);
  if (!box) return;
  box.insertBefore(thoughtNode(row), box.firstChild);
  while (box.childElementCount > 16) box.removeChild(box.lastElementChild);
}

function renderThoughts(id) {
  const box = $(`#th-${id}`);
  if (!box) return;
  box.innerHTML = '';
  for (const row of state.thoughts[id]) box.appendChild(thoughtNode(row));
}

/* ------------------------------ связь с сервером ------------------------------ */
// Обучение живёт в процессе Node (server/trainer.mjs). Страница лишь показывает
// снапшоты и отправляет команды, поэтому обновление вкладки ничего не сбрасывает:
// прогресс, рекорды и мозг хранятся на сервере и на диске.
let snapshotInFlight = false;   // защита от наслоения запросов при медленном сервере

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

// Постоянный индикатор состояния бэкенда: без него пользователь видел «нет связи»
// мелким текстом в KPI и не понимал, что делать (раньше сообщение «запустите node
// serve.js» выводилось один раз при загрузке — и только если сервер не ответил).
function renderServerState() {
  const box = document.getElementById('conn-banner');
  if (!box) return;
  if (state.serverOnline) {
    if (!box.hidden) box.hidden = true;
    return;
  }
  const reason = escapeHtml(state.serverError || 'нет ответа');
  box.hidden = false;
  box.innerHTML =
    `<b>Бэкенд аркады не отвечает</b> (${reason}). Запустите <code>start-arcade.cmd</code> ` +
    `в папке laya_arcade (или <code>start-all.cmd</code> в корне проекта) — адрес: ` +
    `<code>http://127.0.0.1:8099/</code>. Если страница открыта как файл (file://), ` +
    `её нужно открывать по этому адресу.`;
}

async function fetchSnapshot() {
  if (snapshotInFlight) return;      // предыдущий ответ ещё не пришёл — не дублируем запрос
  snapshotInFlight = true;
  try {
    const r = await fetch('/api/snapshot', { cache: 'no-store' });
    if (!r.ok) throw new Error('HTTP ' + r.status);
    const snap = await r.json();
    if (!state.serverOnline) pushLog('связь с сервером аркады установлена', 'record');
    state.serverOnline = true;
    state.serverError = '';
    onSnapshot(snap);
  } catch (e) {
    const wasOnline = state.serverOnline;
    state.serverOnline = false;
    state.serverError = String(e.message);
    if (wasOnline) pushLog(`сервер аркады недоступен: ${state.serverError}`, 'warn');
  } finally {
    snapshotInFlight = false;
  }
  renderServerState();
}

async function apiCmd(cmd, extra = {}) {
  try {
    const r = await fetch('/api/cmd', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ cmd, ...extra }),
    });
    return await r.json();
  } catch (e) {
    pushLog(`сервер не принял команду «${cmd}»: ${e.message}`, 'warn');
    return null;
  }
}

function onSnapshot(snap) {
  state.snapshot = snap.games || {};
  state.totalGenerations = snap.totalGenerations || 0;
  state.paused = !!snap.paused;
  state.focusMode = snap.focusMode || 'best';
  state.serverMeta = {
    lastSavedAt: snap.lastSavedAt,
    restoredFrom: snap.restoredFrom,
    uptimeSec: snap.uptimeSec,
    saves: snap.saves,
  };

  // Карьера Лая (партии, рекорд, уроки) приходит с сервера — страница только показывает.
  state.careers = snap.players || {};

  for (const row of snap.log || []) {
    const record = row.best > (state.lastRecord[row.game] ?? -1);
    if (record) state.lastRecord[row.game] = row.best;
    pushLog(
      `${row.title}: поколение ${row.gen} → ${row.nextGen}: лучший ${row.scoreName} = ${row.best}` +
      `${record ? ' 🏆 новый рекорд' : ''}, средний фитнес ${fmt(row.avgF)}, мутация ${fmt(row.mutationRate, 2)}` +
      (row.skipped ? ` (пропущено записей: ${row.skipped})` : ''),
      record ? 'record' : 'gen'
    );
  }

  for (const [gameId, rows] of Object.entries(snap.thoughts || {})) {
    if (state.view[gameId] !== 'learn') continue;
    for (const row of rows) pushThought(gameId, { ...row, source: 'learn' });
  }

  // Решения Лая (предохранитель, выбор модели, правки) — в ленту карточки
  for (const g of GAMES) {
    const laya = state.snapshot[g.id]?.laya;
    if (!laya || state.view[g.id] !== 'laya') continue;
    const seen = state.seenEvents[g.id] || (state.seenEvents[g.id] = new Set());
    for (const ev of (laya.events || []).slice().reverse()) {
      const key = `${ev.t}|${ev.kind}|${ev.label}|${ev.score}|${ev.games}`;
      if (seen.has(key)) continue;
      seen.add(key);
      if (seen.size > 400) seen.clear();
      pushThought(g.id, {
        t: ev.t,
        thought: ev.thought,
        label: ev.label,
        reward: ev.reward ?? 0,
        fitness: ev.score ?? 0,
        score: ev.score ?? 0,
        kind: ev.kind === 'laya' ? 'laya' : ev.kind === 'seatbelt' ? 'reflex' : 'guess',
        latency: ev.latency || 0,
        source: 'laya',
      });
    }
  }
}

/** Восстановление состояния из снапшота сервера — для отрисовки арены. */
function restoreView(gameId, v) {
  if (!v) return null;
  if (gameId === 'snake') {
    return { snake: v.snake.map(([x, y]) => ({ x, y })), food: { x: v.food[0], y: v.food[1] }, dir: v.dir, alive: v.alive };
  }
  if (gameId === 'mines') {
    return {
      open: Uint8Array.from(v.open), mine: Uint8Array.from(v.mine),
      flag: Uint8Array.from(v.flag), num: Int8Array.from(v.num),
      boom: v.boom, win: v.win,
    };
  }
  return { board: Uint8Array.from(v.board), piece: v.piece, over: v.over, lines: v.lines, points: v.points };
}


/* ------------------------------ карточки игр ------------------------------ */
function buildCards() {
  const host = $('#games');
  host.innerHTML = '';
  for (const g of GAMES) {
    const eng = state.engines[g.id];
    const card = el('section', 'card game-card');
    card.id = `card-${g.id}`;
    card.innerHTML = `
      <div class="card-head">
        <div class="head-left">
          <h2>${eng.title}</h2>
          <p class="sub">${eng.subtitle}</p>
        </div>
        <div class="head-right">
          <button class="btn btn-mini btn-active" id="view-${g.id}">на арене: Лай (GPU)</button>
          <span class="badge" id="badge-${g.id}">обучение…</span>
        </div>
      </div>
      <div class="card-body">
        <div class="arena" style="--aw:${eng.viewport.w}px">
          <canvas id="cv-${g.id}" width="${eng.viewport.w}" height="${eng.viewport.h}"></canvas>
          <div class="arena-foot">
            <span id="foot-${g.id}-score">${g.scoreName}: 0</span>
            <span id="foot-${g.id}-state">—</span>
          </div>
        </div>
        <div class="side">
          <div class="stats" id="st-${g.id}"></div>
          <canvas class="chart" id="ch-${g.id}" width="480" height="80"></canvas>
          <div class="thoughts-head">действия и решения <span class="live">● live</span></div>
          <div class="thoughts" id="th-${g.id}"></div>
          <div class="thoughts-head">карьера Лая: уроки из прошлых партий</div>
          <div class="career" id="career-${g.id}"></div>
        </div>
      </div>`;
    host.appendChild(card);
    state.thoughts[g.id] = [];
  }

  for (const g of GAMES) {
    const btn = document.getElementById(`view-${g.id}`);
    btn.addEventListener('click', () => {
      state.view[g.id] = state.view[g.id] === 'laya' ? 'learn' : 'laya';
      const onLaya = state.view[g.id] === 'laya';
      btn.textContent = onLaya ? 'на арене: Лай (GPU)' : 'на арене: обучаемая сеть';
      btn.classList.toggle('btn-active', onLaya);
      state.thoughts[g.id] = [];
      renderThoughts(g.id);
      pushLog(`${state.engines[g.id].title}: арена переключена на ${onLaya ? 'Лая (RTX 3060)' : 'обучаемую сеть'}`, 'warn');
    });
  }
}

/* ------------------------------ статистика ------------------------------ */
function updateStats() {
  let gens = 0, eps = 0, steps = 0;
  for (const g of GAMES) {
    const s = state.snapshot[g.id];
    if (!s) continue;
    const laya = s.laya;
    gens += s.generation || 0;
    eps += s.episodes || 0;
    steps += s.stepsPerSec || 0;

    const badge = $(`#badge-${g.id}`);
    if (badge) badge.textContent = `поколение ${s.generation} · особь ${s.focusIndex + 1}/${s.size}`;

    const onLaya = state.view[g.id] === 'laya' && laya;
    const shownScore = onLaya ? laya.score : (s.focus ? s.focus.score : 0);
    const footScore = $(`#foot-${g.id}-score`);
    if (footScore) footScore.textContent = `${onLaya ? 'Лай на арене' : 'сеть обучается'}, ${g.scoreName}: ${shownScore}`;

    const footState = $(`#foot-${g.id}-state`);
    if (footState) {
      if (onLaya) {
        const d = laya.lastDecision;
        footState.textContent = d
          ? `${d.kind === 'seatbelt' ? 'предохранитель' : d.kind === 'override' ? 'Лай + правка' : 'Лай'} · ${d.label}${d.latency ? ` · ${d.latency} мс` : ''}`
          : `партия ${laya.games + 1} · счёт ${laya.score}`;
      } else {
        footState.textContent = s.focus?.done
          ? `эпизод завершён · итог ${s.focus.score}`
          : `шаг ${s.focus?.steps ?? 0} · фитнес ${fmt(s.focus?.fitness ?? 0, 2)}`;
      }
    }

    const st = $(`#st-${g.id}`);
    if (st) {
      const rows = [
        ['Лай: рекорд', laya ? laya.best : '—'],
        ['Лай: партий', laya ? laya.games : '—'],
        ['Лай: средний', laya ? laya.avgScore : '—'],
        ['Лай: уроков', laya ? laya.lessons.length : '—'],
        ['Лай: правок', laya ? laya.overrides : '—'],
        ['Лай: сам решил', laya ? laya.layaMoves : '—'],
        ['сеть: поколение', s.generation],
        ['сеть: рекорд', s.bestScore],
        ['сеть: средний ' + g.scoreName, fmt(s.avgScore, 2)],
        ['сеть: эпизодов', s.episodes],
        ['мутация', `${fmt(s.mutationRate, 2)} ± ${fmt(s.mutationAmp, 2)}`],
        ['шагов/сек', s.stepsPerSec],
      ];
      // Обновляем только изменившиеся тексты: пересборка innerHTML каждый такт
      // заставляла браузер пересчитывать раскладку и «дёргала» карточки.
      if (st.childElementCount !== rows.length) {
        st.innerHTML = rows.map(() => '<div class="stat"><span></span><b></b></div>').join('');
      }
      for (let i = 0; i < rows.length; i++) {
        const node = st.children[i];
        if (!node) continue;
        const label = node.firstElementChild, value = node.lastElementChild;
        const [k, v] = rows[i];
        if (label.textContent !== String(k)) label.textContent = k;
        const val = String(v);
        if (value.textContent !== val) value.textContent = val;
      }
    }

    renderCareer(g.id, laya);
  }
  const set = (id, v) => { const n = $(id); if (n) n.textContent = v; };
  set('#kpi-gen', state.totalGenerations);
  set('#kpi-eps', eps);
  set('#kpi-steps', steps);
  const meta = state.serverMeta || {};
  // Время обучения берём у сервера: вкладка живёт меньше, и после F5 счётчик
  // раньше обнулялся, хотя обучение не прерывалось.
  const sec = Number.isFinite(Number(meta.uptimeSec))
    ? Number(meta.uptimeSec)
    : Math.round((performance.now() - state.startedAt) / 1000);
  set('#kpi-time', `${Math.floor(sec / 60)}:${String(sec % 60).padStart(2, '0')}`);
  set('#kpi-server', !state.serverOnline
    ? 'нет связи'
    : (meta.lastSavedAt ? `сохранено ${new Date(meta.lastSavedAt).toLocaleTimeString('ru-RU')}` : 'работает'));
}

/** Карьера Лая: последнее решение с метками, уроки из прошлых партий, правило.
 *  Пересобирается только когда карьера действительно изменилась — иначе блок «дёргался». */
function renderCareer(gameId, laya) {
  const box = document.getElementById(`career-${gameId}`);
  if (!box) return;
  if (!laya) {
    if (state.careerSig[gameId] !== 'none') {
      state.careerSig[gameId] = 'none';
      box.innerHTML = '<div class="th-empty">Лай ещё не начал партию</div>';
    }
    return;
  }
  const top = (laya.lessons || [])[0];
  const sig = `${laya.games}|${laya.best}|${laya.lessons.length}|${top ? top.n : ''}|${laya.overrides}|${laya.layaMoves}|${laya.caution}`;
  if (state.careerSig[gameId] === sig) return;
  state.careerSig[gameId] = sig;

  const lessons = (laya.lessons || []).slice(0, 3).map((l) => `
    <div class="lesson"><span class="lesson-n">партия ${l.n}</span> ${l.note}</div>`).join('');

  box.innerHTML = `
    <div class="career-head">
      <span>партий: <b>${laya.games}</b></span>
      <span>рекорд: <b>${laya.best}</b></span>
      <span>уроков: <b>${laya.lessons.length}</b></span>
      <span>осторожность: <b>${laya.caution}</b></span>
      <span>ходов Лая: <b>${laya.layaMoves}</b></span>
      <span>правок: <b>${laya.overrides}</b></span>
    </div>
    <div class="lessons-title">уроки, которыми Лай пользуется в следующих партиях</div>
    <div class="lessons">${lessons || '<div class="th-empty">уроков пока нет</div>'}</div>`;
}

/* ------------------------------ график ------------------------------ */
function drawChart(cv, history, color) {
  const ctx = cv.getContext('2d');
  const W = cv.width, H = cv.height;
  ctx.clearRect(0, 0, W, H);
  ctx.fillStyle = 'rgba(120,160,240,0.06)';
  ctx.fillRect(0, 0, W, H);
  ctx.strokeStyle = 'rgba(120,160,240,0.14)';
  for (let i = 1; i < 4; i++) {
    const y = (H / 4) * i + 0.5;
    ctx.beginPath(); ctx.moveTo(0, y); ctx.lineTo(W, y); ctx.stroke();
  }
  const hist = (history || []).slice(-40);   // сервер отдаёт компактные массивы [gen, best, avg, bestFit, avgFit]
  if (hist.length < 2) {
    ctx.fillStyle = 'rgba(180,200,255,0.45)';
    ctx.font = '12px ui-monospace, monospace';
    ctx.fillText('кривая обучения появится после первых поколений…', 10, H / 2 + 4);
    return;
  }
  const maxB = Math.max(1, ...hist.map((h) => h[1]));
  const maxF = Math.max(1, ...hist.map((h) => h[4]));
  const line = (pick, max, stroke, fill) => {
    ctx.beginPath();
    hist.forEach((h, i) => {
      const x = (i / (hist.length - 1)) * (W - 8) + 4;
      const y = H - 6 - (pick(h) / max) * (H - 18);
      i ? ctx.lineTo(x, y) : ctx.moveTo(x, y);
    });
    ctx.strokeStyle = stroke;
    ctx.lineWidth = 2;
    ctx.stroke();
    if (fill) {
      ctx.lineTo(W - 4, H - 6); ctx.lineTo(4, H - 6); ctx.closePath();
      ctx.fillStyle = fill; ctx.fill();
    }
  };
  line((h) => h[4], maxF, 'rgba(90,140,255,0.75)', 'rgba(90,140,255,0.10)');
  line((h) => h[1], maxB, color, 'rgba(77,214,255,0.10)');
  ctx.font = '11px ui-monospace, monospace';
  ctx.fillStyle = 'rgba(200,220,255,0.8)';
  ctx.fillText(`рекорд ${maxB}`, 6, 12);
  ctx.fillStyle = 'rgba(150,180,255,0.7)';
  ctx.fillText(`поколений ${hist.length}`, W - 96, 12);
}

/* ------------------------------ арены ------------------------------ */
function renderArenas() {
  for (const g of GAMES) {
    const cv = document.getElementById(`cv-${g.id}`);
    if (!cv) continue;
    const eng = state.engines[g.id];
    const snap = state.snapshot[g.id];
    if (!snap) continue;
    if (state.view[g.id] === 'laya' && snap.laya?.view) {
      eng.render(cv.getContext('2d'), restoreView(g.id, snap.laya.view));
    } else if (snap.focusView) {
      eng.render(cv.getContext('2d'), restoreView(g.id, snap.focusView));
    }
  }
}

/* ------------------------------ цикл отрисовки ------------------------------ */
let statsAt = 0, chartAt = 0;

function frame() {
  const now = performance.now();
  try {
    renderArenas();
    if (now - statsAt > 250) { statsAt = now; updateStats(); }
    if (now - chartAt > 900) {
      chartAt = now;
      for (const g of GAMES) {
        const cv = document.getElementById(`ch-${g.id}`);
        const snap = state.snapshot[g.id];
        if (cv && snap) drawChart(cv, snap.history, g.color);
      }
    }
  } catch (e) {
    // кадр не должен убивать цикл отрисовки: фиксируем причину один раз
    if (!state.frameError) {
      state.frameError = String(e && e.stack ? e.stack : e);
      state.errors.push('кадр: ' + state.frameError);
    }
  }
  requestAnimationFrame(frame);
}


/* ------------------------------ панель флота ------------------------------ */
function renderFleet() {
  const box = $('#fleet-body');
  const badge = $('#fleet-status');
  if (!box || !state.fleet) return;
  const f = state.fleet;
  const s = f.stats();
  if (badge) {
    badge.textContent = s.online === 0
      ? `${s.total} экземпляров не запущено`
      : `${s.online} из ${s.total} на связи · ${s.verdicts} вердиктов`;
  }

  const roles = ['snake', 'mines', 'tetris'];
  const roleName = { snake: 'Змейка', mines: 'Сапёр', tetris: 'Тетрис' };
  const cards = f.agents.map((a) => {
    const st = a.status === 'online' ? 'on' : a.status === 'offline' ? 'off' : 'unk';
    return `
      <div class="l2-game l2-${st}">
        <div class="l2-head"><b><span class="dot dot-${st === 'on' ? 'on' : 'off'}"></span> Лай #${a.id}</b>
          <span>порт ${a.port} · ${a.device || '—'}</span></div>
        <div class="l2-stats">
          <span>эксперт: <b>${roleName[roles[(a.id - 1) % 3]]}</b></span>
          <span>ответов <b>${a.answered}</b></span>
          <span>задержка <b>${a.latency || '—'} мс</b></span>
        </div>
      </div>`;
  }).join('');

  box.innerHTML = `
    <div class="l2-kpi">
      <div><span>экземпляров онлайн</span><b>${s.online} / ${s.total}</b></div>
      <div><span>обдумано ситуаций</span><b>${s.verdicts}</b></div>
      <div><span>средняя задержка</span><b>${fmt(s.avgLatency, 0)} мс</b></div>
      <div><span>карта</span><b>RTX 3060</b></div>
      <div><span>модель</span><b>Laya 322M fp16</b></div>
    </div>
    <div class="l2-games">${cards}</div>
    ${s.online === 0 ? `<p class="muted small"><b>Экземпляры Laya не отвечают.</b>
      Они запускаются вручную: <code>start-all.cmd</code> в корне проекта (сервис 8095 плюс
      инстансы 8101..8105) или <code>powershell -ExecutionPolicy Bypass -File laya_arcade\\tools\\start-laya-fleet.ps1</code>.
      Пока их нет, ходы на аренах выбирают правила игры, а не модель.</p>` : ''}
    <p class="muted small">Каждый Лай — отдельный процесс Go + ONNX со своими весами в VRAM. Ошибок связи: ${f.agents.reduce((n, a) => n + a.errors, 0)}.</p>`;
}

/* ------------------------------ управление ------------------------------ */
function wireControls() {
  $('#btn-pause').addEventListener('click', (e) => {
    const next = !state.paused;
    state.paused = next;
    e.target.textContent = next ? '▶ Продолжить' : '⏸ Пауза';
    apiCmd(next ? 'pause' : 'resume');
    pushLog(next ? 'обучение на сервере поставлено на паузу' : 'обучение на сервере возобновлено', 'warn');
  });

  const speed = $('#speed');
  speed.addEventListener('input', () => {
    state.budgetMs = Number(speed.value);
    $('#speed-val').textContent = `${state.budgetMs} мс/кадр`;
    apiCmd('budget', { value: state.budgetMs });
  });

  $('#btn-focus').addEventListener('click', (e) => {
    state.focusMode = state.focusMode === 'best' ? 'random' : 'best';
    apiCmd('focus', { value: state.focusMode });
    e.target.textContent = state.focusMode === 'best' ? '👁 наблюдаем: лучшая особь' : '👁 наблюдаем: случайная особь';
  });

  $('#btn-save').addEventListener('click', async () => {
    const r = await apiCmd('save');
    pushLog(r?.ok ? 'мозг сохранён на сервере (state/trainer-state.json)' : 'не удалось сохранить мозг на сервере', r?.ok ? 'record' : 'warn');
  });

  $('#btn-load').addEventListener('click', () => {
    // Обучение и так продолжается с серверного состояния: отдельно «загружать» нечего.
    $('#file-brain').click();
  });

  $('#file-brain').addEventListener('change', async (ev) => {
    const file = ev.target.files?.[0];
    if (!file) return;
    try {
      const payload = JSON.parse(await file.text());
      const nets = {}, scores = {};
      for (const g of GAMES) {
        const item = payload.games?.[g.id];
        if (item?.net) { nets[g.id] = item.net; scores[g.id] = item.best || 0; }
      }
      const r = await apiCmd('load', { nets, scores });
      pushLog(r?.ok
        ? `мозг загружен из файла ${file.name}: продолжаю с рекордами ${GAMES.map((g) => `${g.title} ${scores[g.id] || 0}`).join(', ')}`
        : 'сервер не принял мозг из файла', r?.ok ? 'record' : 'warn');
    } catch (e) {
      pushLog(`не удалось прочитать файл мозга: ${e.message}`, 'warn');
    }
    ev.target.value = '';
  });

  $('#btn-export').addEventListener('click', async () => {
    try {
      const r = await fetch('/api/brain', { cache: 'no-store' });
      const payload = await r.json();
      const blob = new Blob([JSON.stringify(payload, null, 1)], { type: 'application/json' });
      const a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = `laya-brain-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-')}.json`;
      a.click();
      URL.revokeObjectURL(a.href);
      pushLog('мозг выгружен в файл (с сервера)', 'record');
    } catch (e) {
      pushLog(`не удалось выгрузить мозг: ${e.message}`, 'warn');
    }
  });

  $('#btn-reset').addEventListener('click', async () => {
    await apiCmd('reset');
    state.lastRecord = {};
    pushLog('обучение на сервере начато с нуля: случайные веса, поколение 1', 'warn');
  });

  document.addEventListener('keydown', (e) => {
    if (e.code === 'Space') { e.preventDefault(); $('#btn-pause').click(); }
  });
}

/* ------------------------------ запуск ------------------------------ */
export function boot() {
  for (const g of GAMES) state.engines[g.id] = g.make();

  state.fleet = new LayaFleet(LAYA_PORTS);
  state.fleet.onUpdate = renderFleet;

  buildCards();
  wireControls();
  renderJournal();

  window.addEventListener('error', (e) => {
    state.errors.push(String(e.message));
    pushLog(`ошибка страницы: ${e.message}`, 'warn');
  });

  // Обучение И игра Лаев идут на сервере: страница только подписывается на снапшоты.
  fetchSnapshot().then(() => {
    if (state.serverOnline) {
      const meta = state.serverMeta || {};
      const restored = meta.restoredFrom ? new Date(meta.restoredFrom).toLocaleString('ru-RU') : 'нет';
      pushLog('подключено к серверу: обучение и игра Лаев идут независимо от этой вкладки', 'record');
      pushLog(`состояние на сервере: поколений ${state.totalGenerations}, сохранение на диск — ${meta.lastSavedAt ? new Date(meta.lastSavedAt).toLocaleTimeString('ru-RU') : 'ещё не было'}, восстановлено из — ${restored}`, 'info');
      const lessons = GAMES.reduce((n, g) => n + (state.careers[g.id]?.lessons?.length || 0), 0);
      pushLog(`память Лаев: ${lessons} уроков из прошлых партий — они подаются модели в следующих играх`, lessons ? 'record' : 'info');
    } else {
      pushLog(`сервер недоступен: ${state.serverError || 'нет ответа'} — запустите node serve.js`, 'warn');
    }
  });

  setInterval(fetchSnapshot, 400);                        // снапшоты обучения и игры (реже — меньше нагрузки)
  setInterval(() => { try { updateStats(); } catch (_) {} }, 700);  // метрики видны и в неактивной вкладке
  setInterval(() => state.fleet.probeAll(), 6000);        // состояние экземпляров Laya

  requestAnimationFrame(frame);

  window.__laya = {
    state,
    server: () => ({
      online: state.serverOnline,
      totalGenerations: state.totalGenerations,
      meta: state.serverMeta,
      careers: Object.fromEntries(Object.entries(state.careers || {}).map(([k, c]) => [k, c ? { games: c.games, best: c.best, lessons: c.lessons?.length || 0, caution: c.caution } : null])),
    }),
    fleet: () => state.fleet.agents.map((a) => ({ id: a.id, port: a.port, status: a.status, device: a.device, answered: a.answered, latency: a.latency, errors: a.errors })),
    stats: () => GAMES.map((g) => {
      const s = state.snapshot[g.id] || {};
      return {
        game: g.id,
        generation: s.generation, episodes: s.episodes, bestScore: s.bestScore,
        avgScore: s.avgScore && +(+s.avgScore).toFixed(2), stepsPerSec: s.stepsPerSec,
        focus: s.focus,
        laya: s.laya ? {
          games: s.laya.games, bestScore: s.laya.best, currentScore: s.laya.score,
          lessons: s.laya.lessons.length, caution: s.laya.caution,
          layaMoves: s.laya.layaMoves, seatbelt: s.laya.seatbeltMoves, overrides: s.laya.overrides,
          lastDecision: s.laya.lastDecision,
        } : null,
        view: state.view[g.id],
      };
    }),
    lessons: (id) => (state.snapshot[id]?.laya?.lessons || []).map((l) => l.note),
    thoughts: (id) => state.thoughts[id]?.slice(0, 3),
    log: () => state.log.slice(0, 20),
    errors: () => state.errors,
  };
}
