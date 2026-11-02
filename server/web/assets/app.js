"use strict";

// Laya Work UI. Никаких внешних библиотек и innerHTML для данных: всё, что
// пришло из событий, выводится через textContent. Графики — SVG, поэтому CSP
// может запрещать inline-стили.

const state = {
  filters: {},
  events: [],
  seqs: new Set(),
  lastSeq: 0,
  beforeSeq: 0,
  paused: false,
  pending: [],
  overview: null,
  detailId: null,
  stream: null,
  reloadTimer: null,
};

const $ = (id) => document.getElementById(id);
const NS = "http://www.w3.org/2000/svg";

function el(tag, className, textContent) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (textContent !== undefined && textContent !== null) node.textContent = String(textContent);
  return node;
}

function svgEl(tag, attrs) {
  const node = document.createElementNS(NS, tag);
  for (const [k, v] of Object.entries(attrs || {})) node.setAttribute(k, String(v));
  return node;
}

function clear(node) {
  while (node.firstChild) node.removeChild(node.firstChild);
}

function toast(message, isError = false) {
  const t = $("toast");
  t.textContent = message;
  t.className = "toast" + (isError ? " err" : "");
  t.hidden = false;
  clearTimeout(toast._timer);
  toast._timer = setTimeout(() => { t.hidden = true; }, 4200);
}

async function api(path, options) {
  const res = await fetch(path, options);
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { data = null; }
  if (!res.ok) {
    const msg = data && (data.error || data.message) ? (data.error || data.message) : `HTTP ${res.status}`;
    throw new Error(msg);
  }
  return data;
}

// ---------------------------------------------------------------- filters

function readFilters() {
  const q = new URLSearchParams(location.search);
  return {
    range: q.get("range") || "24h",
    session: q.get("session") || "",
    operation: q.get("operation") || "",
    status: q.get("status") || "",
    checkpoint: q.get("checkpoint") || "",
    error: q.get("error") === "1",
    fallback: q.get("fallback") === "1",
    truncated: q.get("truncated") === "1",
    labeled: q.get("labeled") === "1",
    demo: q.get("demo") === "1",
  };
}

function writeFilters() {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(state.filters)) {
    if (k === "range") { if (v !== "24h") p.set(k, v); continue; }
    if (typeof v === "boolean") { if (v) p.set(k, "1"); continue; }
    if (v) p.set(k, v);
  }
  const qs = p.toString();
  history.replaceState(null, "", qs ? `?${qs}` : location.pathname);
}

function applyFiltersToUI() {
  $("f-range").value = state.filters.range;
  $("f-session").value = state.filters.session;
  $("f-operation").value = state.filters.operation;
  $("f-status").value = state.filters.status;
  $("f-checkpoint").value = state.filters.checkpoint;
  $("f-error").checked = state.filters.error;
  $("f-fallback").checked = state.filters.fallback;
  $("f-truncated").checked = state.filters.truncated;
  $("f-labeled").checked = state.filters.labeled;
  $("f-demo").checked = state.filters.demo;
}

function collectFilters() {
  state.filters = {
    range: $("f-range").value,
    session: $("f-session").value.trim(),
    operation: $("f-operation").value.trim(),
    status: $("f-status").value,
    checkpoint: $("f-checkpoint").value.trim(),
    error: $("f-error").checked,
    fallback: $("f-fallback").checked,
    truncated: $("f-truncated").checked,
    labeled: $("f-labeled").checked,
    demo: $("f-demo").checked,
  };
  writeFilters();
  updateExportLink();
}

function queryString(withLimit = true) {
  const p = new URLSearchParams();
  p.set("range", state.filters.range);
  for (const k of ["session", "operation", "status", "checkpoint"]) {
    if (state.filters[k]) p.set(k, state.filters[k]);
  }
  for (const k of ["error", "fallback", "truncated", "labeled", "demo"]) {
    if (state.filters[k]) p.set(k, "1");
  }
  if (withLimit) p.set("limit", "50");
  return p.toString();
}

function updateExportLink() {
  $("export-link").href = `/api/work/v1/export?${queryString(false)}&format=jsonl`;
}

// ---------------------------------------------------------------- overview

async function loadOverview() {
  const data = await api(`/api/work/v1/overview?${queryString(false)}`);
  state.overview = data;
  renderServer(data.server, data.work);
  renderSummary(data.totals);
  renderTimeline(data.timeline || []);
  renderBars("chart-selected", data.selected || [], "выбор");
  renderBars("chart-status", data.by_status || [], "статус");
  renderBars("chart-operations", data.by_operation || [], "операция");
  renderLatency(data.latency || {});
  renderAccuracy(data.accuracy || {});
  renderSavings(data.savings || []);
  renderBanners(data);
  renderOperationsList(data.by_operation || []);
  const from = new Date(data.range.from).toLocaleString("ru-RU");
  const to = new Date(data.range.to).toLocaleString("ru-RU");
  $("range-hint").textContent = `${from} — ${to}`;
}

function renderServer(server, work) {
  const dot = $("server-dot");
  dot.className = "brand-dot " + (server && server.ready ? "ok" : "bad");
  $("server-state").textContent = server && server.ready ? "готов" : "не готов";
  $("server-checkpoint").textContent = `checkpoint ${server?.checkpoint || "—"}`;
  $("server-device").textContent = `device ${server?.device || "—"}`;
  $("server-uptime").textContent = `uptime ${server?.uptime_s ?? "—"} с`;
  const dropped = work?.dropped || 0;
  const writeErrors = work?.write_errors || 0;
  const spoolErrors = work?.spool_errors || 0;
  const banner = $("store-banner");
  if (dropped || writeErrors || spoolErrors) {
    banner.hidden = false;
    banner.textContent = `Телеметрия: потеряно ${dropped}, ошибок записи ${writeErrors}, ошибок spool ${spoolErrors}. Решения продолжают работать; часть истории могла не сохраниться.`;
  } else {
    banner.hidden = true;
  }
}

function renderSummary(t) {
  $("kpi-inference").textContent = fmtInt(t.inference_calls);
  $("kpi-rule").textContent = fmtInt(t.rule_only);
  $("kpi-errors").textContent = fmtInt(t.errors + t.timeouts);
  $("kpi-errors-note").textContent = t.client_errors ? `клиентских ошибок: ${fmtInt(t.client_errors)}` : "inference error + timeout";
  $("kpi-fallback").textContent = fmtInt(t.fallbacks);
  $("kpi-p50-inf").textContent = fmtMs(state.overview?.latency?.inference?.p50);
  $("kpi-p95-inf").textContent = fmtMs(state.overview?.latency?.inference?.p95);
  $("kpi-p50-total").textContent = fmtMs(state.overview?.latency?.total?.p50);
  $("kpi-p95-total").textContent = fmtMs(state.overview?.latency?.total?.p95);
  $("kpi-truncated").textContent = fmtInt(t.truncated);
  $("kpi-dropped").textContent = fmtInt((state.overview?.work?.dropped || 0) + (state.overview?.work?.spool_errors || 0));
}

function renderBanners(ov) {
  const demo = $("demo-banner");
  demo.hidden = !(ov.totals && ov.totals.demo > 0);
}

function renderOperationsList(items) {
  const list = $("operations-list");
  clear(list);
  for (const item of items) {
    const opt = document.createElement("option");
    opt.value = item.label;
    list.appendChild(opt);
  }
}

// ---------------------------------------------------------------- charts

function emptyChart(id, text) {
  const box = $(id);
  clear(box);
  box.appendChild(el("div", "chart-empty", text || "нет данных"));
}

function renderTimeline(buckets) {
  if (!buckets.length) return emptyChart("chart-timeline", "нет событий за период");
  const box = $("chart-timeline");
  clear(box);
  const w = 900, h = 190, padL = 34, padR = 12, padT = 12, padB = 26;
  const innerW = w - padL - padR, innerH = h - padT - padB;
  const max = Math.max(1, ...buckets.map(b => (b.model || 0) + (b.policy || 0) + (b.error || 0)));
  const bw = innerW / buckets.length;
  const svg = svgEl("svg", { viewBox: `0 0 ${w} ${h}`, role: "img", "aria-label": "Запросы по времени" });
  for (let i = 0; i <= 4; i++) {
    const y = padT + innerH - (innerH * i) / 4;
    svg.appendChild(svgEl("line", { x1: padL, x2: w - padR, y1: y, y2: y, stroke: "rgba(120,145,190,0.12)" }));
    const t = svgEl("text", { x: 4, y: y + 3, fill: "#516078", "font-size": 9 });
    t.textContent = String(Math.round((max * i) / 4));
    svg.appendChild(t);
  }
  buckets.forEach((b, i) => {
    const x = padL + i * bw + Math.max(1, bw * 0.16);
    const width = Math.max(1, bw * 0.68);
    const model = b.model || 0, policy = b.policy || 0, err = b.error || 0;
    let y = padT + innerH;
    for (const [value, color] of [[model, "#38bdf8"], [policy, "#5eead4"], [err, "#f87171"]]) {
      if (!value) continue;
      const hh = (value / max) * innerH;
      y -= hh;
      svg.appendChild(svgEl("rect", { x, y, width, height: hh, fill: color, rx: 1 }));
    }
  });
  const first = buckets[0]?.t, last = buckets[buckets.length - 1]?.t;
  for (const [x, val] of [[padL, first], [w - padR, last]]) {
    const t = svgEl("text", { x, y: h - 6, fill: "#516078", "font-size": 9, "text-anchor": x === padL ? "start" : "end" });
    t.textContent = new Date(val).toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit" });
    svg.appendChild(t);
  }
  box.appendChild(svg);
  const legend = el("div", "muted small");
  legend.textContent = "■ engine-вызовы   ■ policy   ■ ошибки";
  box.appendChild(legend);
}

function renderBars(id, items, label) {
  if (!items.length) return emptyChart(id, `нет данных: ${label}`);
  const box = $(id);
  clear(box);
  const w = 420, rowH = 24, h = Math.max(60, items.length * rowH + 12);
  const max = Math.max(1, ...items.map(i => i.count || 0));
  const svg = svgEl("svg", { viewBox: `0 0 ${w} ${h}`, role: "img", "aria-label": label });
  items.slice(0, 12).forEach((it, i) => {
    const y = 4 + i * rowH;
    const labelW = 130;
    const bw = ((it.count || 0) / max) * (w - labelW - 60);
    const t = svgEl("text", { x: 0, y: y + 13, fill: "#b7c3d6", "font-size": 10 });
    t.textContent = String(it.label || "—").slice(0, 22);
    svg.appendChild(t);
    svg.appendChild(svgEl("rect", { x: labelW, y: y + 2, width: Math.max(1, bw), height: 14, rx: 2, fill: "#38bdf8" }));
    const v = svgEl("text", { x: w - 2, y: y + 13, fill: "#7d8ba1", "font-size": 10, "text-anchor": "end" });
    v.textContent = String(it.count);
    svg.appendChild(v);
  });
  box.appendChild(svg);
}

function renderLatency(lat) {
  const inf = lat.inference || {}, total = lat.total || {};
  const box = $("chart-latency");
  clear(box);
  const rows = [
    ["inference p50", inf.p50], ["inference p95", inf.p95],
    ["операция p50", total.p50], ["операция p95", total.p95],
  ];
  const max = Math.max(1, ...rows.map(r => Number(r[1]) || 0));
  const w = 420, rowH = 30, h = rows.length * rowH + 8;
  const svg = svgEl("svg", { viewBox: `0 0 ${w} ${h}`, role: "img", "aria-label": "Задержки" });
  rows.forEach(([label, value], i) => {
    const y = 4 + i * rowH;
    const t = svgEl("text", { x: 0, y: y + 13, fill: "#b7c3d6", "font-size": 10 });
    t.textContent = label;
    svg.appendChild(t);
    svg.appendChild(svgEl("rect", { x: 120, y: y + 2, width: Math.max(1, ((Number(value) || 0) / max) * 240), height: 14, rx: 2, fill: i < 2 ? "#38bdf8" : "#5eead4" }));
    const v = svgEl("text", { x: w - 2, y: y + 13, fill: "#7d8ba1", "font-size": 10, "text-anchor": "end" });
    v.textContent = fmtMs(value);
    svg.appendChild(v);
  });
  box.appendChild(svg);
}

function renderAccuracy(acc) {
  const headline = $("accuracy-headline");
  const note = $("accuracy-note");
  const mae = $("accuracy-mae");
  const labeled = acc.labeled || 0;
  if (!labeled) {
    headline.textContent = "Без разметки";
    note.textContent = `Нет размеченных вопросов. Всего inference-вызовов: ${fmtInt(state.overview?.totals?.inference_calls || 0)}.`;
    mae.textContent = "Уверенность модели не равна правильности.";
  } else {
    headline.textContent = `Точность: ${(acc.accuracy * 100).toFixed(1)}% · ${acc.matches} из ${labeled} размеченных вопросов`;
    note.textContent = `Размечено вызовов: ${acc.labeled_calls ?? "—"}; без разметки: ${fmtInt(state.overview?.totals?.unlabeled || 0)} inference-вызовов.`;
    mae.textContent = acc.mae === undefined || acc.mae === null ? "MAE: только для числовых меток" : `MAE: ${Number(acc.mae).toFixed(3)}`;
  }
  const box = $("confusion");
  clear(box);
  const labels = acc.labels || [];
  if (!labels.length) {
    box.textContent = "нет размеченных примеров";
  } else {
    const table = document.createElement("table");
    table.className = "confusion";
    const thead = document.createElement("thead");
    const hr = document.createElement("tr");
    hr.appendChild(el("th", "", "ожидалось ↓ / выбрано →"));
    for (const l of labels) hr.appendChild(el("th", "", l));
    thead.appendChild(hr);
    table.appendChild(thead);
    const tbody = document.createElement("tbody");
    const flat = (acc.confusion || []).flat();
    const max = Math.max(1, ...flat);
    (acc.confusion || []).forEach((row, i) => {
      const tr = document.createElement("tr");
      tr.appendChild(el("th", "rowhead", labels[i]));
      row.forEach((v) => {
        const td = el("td", `heat-${Math.min(4, Math.ceil((v / max) * 4))}`, String(v));
        tr.appendChild(td);
      });
      tbody.appendChild(tr);
    });
    table.appendChild(tbody);
    box.appendChild(table);
  }
  const pc = $("per-class");
  clear(pc);
  if (!acc.per_class || !acc.per_class.length) {
    pc.textContent = "нет данных";
  } else {
    const table = document.createElement("table");
    table.className = "perclass";
    const thead = document.createElement("thead");
    const hr = document.createElement("tr");
    for (const h of ["класс", "precision", "recall", "support"]) hr.appendChild(el("th", "", h));
    thead.appendChild(hr);
    table.appendChild(thead);
    const tbody = document.createElement("tbody");
    for (const row of acc.per_class) {
      const tr = document.createElement("tr");
      tr.appendChild(el("td", "", row.label));
      tr.appendChild(el("td", "", (row.precision * 100).toFixed(1) + "%"));
      tr.appendChild(el("td", "", (row.recall * 100).toFixed(1) + "%"));
      tr.appendChild(el("td", "", fmtInt(row.support)));
      tbody.appendChild(tr);
    }
    table.appendChild(tbody);
    pc.appendChild(table);
  }
}

function renderSavings(items) {
  const wrap = $("savings-wrap");
  const box = $("savings");
  clear(box);
  if (!items.length) {
    wrap.hidden = true;
    return;
  }
  wrap.hidden = false;
  for (const it of items) {
    const card = el("div", "savings-item");
    card.appendChild(el("strong", "", it.tool));
    card.appendChild(el("span", "", `событий: ${fmtInt(it.events)}`));
    card.appendChild(el("span", "", `исходный материал: ${fmtBytes(it.source_bytes)}`));
    card.appendChild(el("span", "", `возвращено: ${fmtBytes(it.returned_bytes)}`));
    card.appendChild(el("span", "", `дочитывания laya_get_chunk: ${fmtBytes(it.followup_bytes)}`));
    box.appendChild(card);
  }
}

// ---------------------------------------------------------------- feed

async function loadEvents(reset) {
  if (reset) {
    state.events = [];
    state.seqs.clear();
    state.beforeSeq = 0;
    $("events-body").replaceChildren();
  }
  const p = new URLSearchParams(queryString(true));
  if (state.beforeSeq) p.set("before_seq", String(state.beforeSeq));
  const data = await api(`/api/work/v1/events?${p.toString()}`);
  state.lastSeq = Math.max(state.lastSeq, data.last_seq || 0);
  for (const item of data.events || []) addEventRow(item.event, item.seq, false);
  state.beforeSeq = data.next_before_seq || 0;
  $("load-more").hidden = !state.beforeSeq;
  $("empty-state").hidden = state.events.length > 0;
  writeFilters();
  updateExportLink();
}

function addEventRow(ev, seq, prepend) {
  if (!ev || state.seqs.has(seq)) return;
  state.seqs.add(seq);
  const row = buildRow(ev, seq);
  const body = $("events-body");
  if (prepend) body.insertBefore(row, body.firstChild);
  else body.appendChild(row);
  if (prepend) state.events.unshift({ ev, seq });
  else state.events.push({ ev, seq });
}

function buildRow(ev, seq) {
  const tr = document.createElement("tr");
  tr.tabIndex = 0;
  tr.addEventListener("click", () => openDetail(ev.event_id));
  tr.addEventListener("keydown", (e) => { if (e.key === "Enter") openDetail(ev.event_id); });
  const time = el("td", "cell-mono", fmtTime(ev.timestamp));
  if (ev.demo) time.appendChild(el("span", "cell-sub", "demo"));
  tr.appendChild(time);
  tr.appendChild(el("td", "", shortSession(ev.session_id)));
  const op = el("td", "", ev.operation || ev.kind || "—");
  op.appendChild(el("span", "cell-sub", `${ev.source || "?"} / ${ev.kind || "?"}`));
  tr.appendChild(op);
  const input = el("td", "cell-input", preview(inputText(ev)));
  input.title = inputText(ev);
  tr.appendChild(input);
  tr.appendChild(el("td", "", modelSelection(ev)));
  tr.appendChild(el("td", "", policyText(ev)));
  const lat = ev.timing_ms || {};
  tr.appendChild(el("td", "cell-mono", fmtMs(lat.inference || lat.total)));
  const st = el("td", "");
  st.appendChild(statusBadge(ev.status, ev));
  tr.appendChild(st);
  return tr;
}

function modelSelection(ev) {
  const mr = ev.model_result || {};
  if (mr.selected) return String(mr.selected);
  if (mr.error) return "ошибка";
  return "—";
}

function policyText(ev) {
  const pr = ev.policy_result || {};
  if (pr.action) return String(pr.action);
  if (ev.kind === "tool_result") return "результат";
  if (ev.kind === "agent_action") return "агент";
  return "—";
}

function statusBadge(status, ev) {
  const s = status || (ev?.model_result?.error ? "error" : "completed");
  const cls = s === "completed" ? "badge-ok" : s === "error" ? "badge-err" : s === "timeout" ? "badge-warn" : s === "fallback" ? "badge-warn" : s === "skipped" ? "badge-muted" : "badge-info";
  const b = el("span", `badge ${cls}`, s);
  if (ev?.truncated) b.appendChild(el("span", "badge-muted", " trunc"));
  return b;
}

// ---------------------------------------------------------------- detail

async function openDetail(id) {
  if (!id) return;
  state.detailId = id;
  try {
    const data = await api(`/api/work/v1/events/${encodeURIComponent(id)}`);
    renderDetail(data);
    $("drawer").classList.add("open");
    $("drawer").setAttribute("aria-hidden", "false");
    $("drawer-overlay").hidden = false;
  } catch (err) {
    toast(`Не удалось открыть событие: ${err.message}`, true);
  }
}

function closeDetail() {
  $("drawer").classList.remove("open");
  $("drawer").setAttribute("aria-hidden", "true");
  $("drawer-overlay").hidden = true;
  state.detailId = null;
}

function renderDetail(data) {
  const ev = data.event || {};
  const related = data.related || [];
  $("detail-eyebrow").textContent = `${ev.source || "?"} / ${ev.kind || "?"} · seq ${data.seq}`;
  $("detail-title").textContent = ev.operation || ev.event_id;
  const body = $("detail-body");
  clear(body);

  const meta = section("Идентификаторы");
  meta.appendChild(detailGrid([
    ["event_id", ev.event_id],
    ["trace_id", ev.trace_id],
    ["call_id", ev.call_id || "—"],
    ["session_id", ev.session_id || "—"],
    ["operation", ev.operation || "—"],
    ["status", ev.status || "—"],
    ["checkpoint", ev.model?.checkpoint || "—"],
    ["policy_version", ev.policy_version || "—"],
    ["timestamp", ev.timestamp || "—"],
    ["demo", ev.demo ? "да" : "нет"],
  ]));
  body.appendChild(meta);

  const input = section("Что поступило и что ушло модели");
  input.appendChild(textBlock("Получено (после маскирования)", ev.input?.original_redacted, ev.text_saved));
  input.appendChild(textBlock("Отправлено в /decide (после подготовки)", ev.input?.effective_redacted, ev.text_saved));
  input.appendChild(detailGrid([
    ["символов получено", ev.input?.original_chars ?? ev.input?.chars ?? "—"],
    ["символов отправлено", ev.input?.effective_chars ?? ev.input?.chars ?? "—"],
    ["токенов получено", ev.input?.received_tokens ?? ev.input?.token_count ?? "—"],
    ["токенов обработано моделью", ev.input?.processed_tokens ?? "—"],
    ["токенов отброшено", ev.input?.dropped_tokens ?? "—"],
    ["усечение", ev.truncated || ev.input?.truncated ? "да" : "нет"],
    ["текст сохранён", ev.text_saved === false ? "нет" : "да"],
    ["сокращено для хранения", ev.input?.storage_truncated ? "да" : "нет"],
  ]));
  body.appendChild(input);

  const qs = ev.questions || [];
  if (qs.length) {
    const qsec = section("Вопросы и ответ модели");
    qs.forEach((q) => qsec.appendChild(questionCard(ev, q)));
    body.appendChild(qsec);
  }

  const pol = section("Решение плагина");
  pol.appendChild(policyBlock(ev));
  body.appendChild(pol);

  const timing = section("Тайминги");
  const t = ev.timing_ms || {};
  timing.appendChild(detailGrid([
    ["count", fmtMs(t.count)],
    ["inference", fmtMs(t.inference)],
    ["total", fmtMs(t.total)],
  ]));
  body.appendChild(timing);

  const evsec = section("Evidence и проверки");
  evsec.appendChild(evidenceBlock(ev));
  body.appendChild(evsec);

  if (related.length) {
    const rel = section("Связанные события");
    const list = el("div", "related-list");
    for (const item of related) {
      const row = el("div", "related-item");
      const left = el("div");
      left.appendChild(el("div", "", `${item.relation}: ${item.event.operation || item.event.kind}`));
      left.appendChild(el("div", "muted", `${item.event.status || "—"} · ${fmtTime(item.event.timestamp)} · ${item.event.event_id}`));
      row.appendChild(left);
      row.appendChild(el("span", "muted", `seq ${item.seq}`));
      row.addEventListener("click", () => openDetail(item.event.event_id));
      list.appendChild(row);
    }
    rel.appendChild(list);
    body.appendChild(rel);
  }

  body.appendChild(labelSection(ev));
  body.appendChild(jsonSection(data));
}

function section(title) {
  const s = el("section", "detail-section");
  s.appendChild(el("h3", "", title));
  return s;
}

function detailGrid(rows) {
  const grid = el("div", "detail-grid");
  for (const [k, v] of rows) {
    const d = document.createElement("div");
    d.appendChild(el("span", "", k + ": "));
    d.appendChild(el("strong", "", v === undefined || v === null || v === "" ? "—" : String(v)));
    grid.appendChild(d);
  }
  return grid;
}

function textBlock(title, text, textSaved) {
  const wrap = document.createElement("div");
  wrap.appendChild(el("div", "muted small", title));
  if (textSaved === false && !text) {
    wrap.appendChild(el("div", "text-missing", "текст не сохранялся (старая запись)"));
  } else {
    const pre = el("pre", "text-block", text || "—");
    wrap.appendChild(pre);
  }
  wrap.className = "text-block-wrap";
  return wrap;
}

function questionCard(ev, q) {
  const card = el("div", "question-card");
  card.appendChild(el("h4", "", `${q.id || "question"} · ${q.type || "?"}`));
  card.appendChild(el("div", "instructions", q.instructions || "—"));
  const ans = answerFor(ev, q.id);
  const selected = selectedOf(ans);
  // noul приходит числом 0/1, а варианты подписаны false/true. Показываем
  // выбранным именно вариант, а не числовой ярлык.
  let chosen = selected;
  if (q.type === "noul" && /^(0|1)(\.0+)?$/.test(String(selected))) chosen = Number(selected) ? "true" : "false";
  const opts = el("div", "option-list");
  const matchesChosen = (label) => chosen && (label === chosen || label.startsWith(chosen + ":"));
  for (const opt of q.options || []) {
    const label = String(opt);
    const cls = "option" + (matchesChosen(label) ? " chosen" : "");
    opts.appendChild(el("span", cls, label));
  }
  if (chosen && !(q.options || []).some((o) => matchesChosen(String(o)))) {
    opts.appendChild(el("span", "option chosen", `выбрано: ${chosen}`));
  }
  card.appendChild(opts);
  const probs = ans?.probabilities || [];
  if (probs.length) {
    for (const p of probs) {
      const row = el("div", "prob-row");
      row.appendChild(el("span", "", String(p.label)));
      const bar = document.createElement("progress");
      bar.max = 1;
      bar.value = Number(p.value) || 0;
      row.appendChild(bar);
      row.appendChild(el("span", "", fmtProb(p.value)));
      card.appendChild(row);
    }
  } else if (selected) {
    card.appendChild(el("div", "muted small", `ответ: ${selected}`));
  }
  if (ans && ans.answer_confidence !== undefined) {
    card.appendChild(el("div", "muted small", `answer_confidence: ${fmtProb(ans.answer_confidence)}`));
  }
  return card;
}

function answerFor(ev, qid) {
  const mr = ev.model_result || {};
  if (mr.answers && mr.answers[qid]) return mr.answers[qid];
  if (mr.scores && mr.scores[qid]) return mr.scores[qid];
  if (mr.selected_by_question && mr.selected_by_question[qid] !== undefined) return { choice: mr.selected_by_question[qid] };
  if (mr.selected !== undefined && (!ev.questions || ev.questions.length <= 1)) return { choice: String(mr.selected) };
  return null;
}

function selectedOf(ans) {
  if (!ans) return "";
  if (ans.choice !== undefined && ans.choice !== null) return String(ans.choice);
  if (ans.score !== undefined && ans.score !== null) return String(ans.score);
  if (ans.noul !== undefined && ans.noul !== null) return String(ans.noul);
  return "";
}

function policyBlock(ev) {
  const wrap = document.createElement("div");
  const pr = ev.policy_result || {};
  const line = el("div", "policy-box");
  line.appendChild(el("span", "policy-action", pr.action || "не записано"));
  if (pr.basis) line.appendChild(el("span", "badge badge-info", `basis: ${pr.basis}`));
  if (pr.laya_called === false) line.appendChild(el("span", "badge badge-muted", "Laya не вызывалась"));
  if (pr.fallback) line.appendChild(el("span", "badge badge-warn", "fallback"));
  wrap.appendChild(line);
  if (pr.reason) wrap.appendChild(el("div", "policy-reason", pr.reason));
  const rules = pr.rule_ids || pr.rules || [];
  if (Array.isArray(rules) && rules.length) {
    const list = el("div", "rule-list");
    for (const r of rules) list.appendChild(el("span", "rule", String(r)));
    wrap.appendChild(list);
  }
  if (!pr.action && !pr.reason) {
    wrap.appendChild(el("div", "muted small", "Плагин не приложил policy_result; смотрите model_result и evidence."));
  }
  return wrap;
}

function evidenceBlock(ev) {
  const wrap = document.createElement("div");
  const evd = ev.evidence;
  if (!evd || (typeof evd === "object" && !Array.isArray(evd) && Object.keys(evd).length === 0)) {
    wrap.appendChild(el("div", "muted small", "нет evidence"));
    return wrap;
  }
  const list = el("div", "evidence-list");
  if (Array.isArray(evd)) {
    for (const item of evd) {
      const div = el("div", "evidence-item");
      div.textContent = typeof item === "string" ? item : JSON.stringify(item);
      list.appendChild(div);
    }
  } else if (typeof evd === "object") {
    for (const [k, v] of Object.entries(evd)) {
      const div = el("div", "evidence-item");
      div.appendChild(el("span", "muted", k + ": "));
      div.appendChild(el("span", "", typeof v === "object" ? JSON.stringify(v) : String(v)));
      list.appendChild(div);
    }
  } else {
    list.appendChild(el("div", "evidence-item", String(evd)));
  }
  wrap.appendChild(list);
  return wrap;
}

function labelSection(ev) {
  const sec = section("Ручная разметка");
  const labels = ev.labels || [];
  if (labels.length) {
    const msg = el("div", "muted small");
    msg.textContent = `Уже размечено вопросов: ${labels.length}. Сохранение перезапишет метку выбранного вопроса.`;
    sec.appendChild(msg);
  }
  const form = el("div", "label-form");
  const qs = ev.questions && ev.questions.length ? ev.questions : [{ id: "default", type: "choice", options: [] }];
  const qLabel = el("label", "", "Вопрос");
  const qSelect = document.createElement("select");
  for (const q of qs) {
    const opt = document.createElement("option");
    opt.value = q.id || "default";
    opt.textContent = `${q.id || "default"} · ${q.type || "?"}`;
    qSelect.appendChild(opt);
  }
  qLabel.appendChild(qSelect);
  form.appendChild(qLabel);

  const expLabel = el("label", "", "Ожидаемый ответ");
  const expWrap = document.createElement("div");
  expLabel.appendChild(expWrap);
  form.appendChild(expLabel);

  const authorLabel = el("label", "", "Автор");
  const author = document.createElement("input");
  author.type = "text";
  author.value = "local";
  authorLabel.appendChild(author);
  form.appendChild(authorLabel);

  const basisLabel = el("label", "", "Основание");
  const basis = document.createElement("input");
  basis.type = "text";
  basis.placeholder = "эталон, правило, ручная проверка";
  basisLabel.appendChild(basis);
  form.appendChild(basisLabel);

  const commentLabel = el("label", "full", "Комментарий");
  const comment = document.createElement("textarea");
  comment.placeholder = "почему этот ответ правильный";
  commentLabel.appendChild(comment);
  form.appendChild(commentLabel);

  function buildExpected() {
    clear(expWrap);
    const q = qs.find(x => (x.id || "default") === qSelect.value) || qs[0];
    const existing = labels.find(l => l.question_id === (q.id || "default"));
    if (q.options && q.options.length) {
      const sel = document.createElement("select");
      for (const o of q.options) {
        const opt = document.createElement("option");
        opt.value = String(o);
        opt.textContent = String(o);
        if (existing && existing.expected === String(o)) opt.selected = true;
        sel.appendChild(opt);
      }
      expWrap.appendChild(sel);
    } else {
      const input = document.createElement("input");
      input.type = "text";
      input.value = existing ? existing.expected : "";
      input.placeholder = q.type === "score" ? "число" : "значение";
      expWrap.appendChild(input);
    }
  }
  qSelect.addEventListener("change", buildExpected);
  buildExpected();

  const save = el("button", "btn btn-accent full", "Сохранить метку");
  save.type = "button";
  save.addEventListener("click", async () => {
    const qid = qSelect.value;
    const control = expWrap.querySelector("input,select");
    const expected = control ? String(control.value).trim() : "";
    if (!expected) { toast("Укажите ожидаемый ответ", true); return; }
    try {
      await api("/api/work/v1/labels", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          call_id: ev.call_id || ev.event_id,
          question_id: qid,
          expected,
          author: author.value.trim() || "local",
          comment: comment.value.trim(),
          basis: basis.value.trim(),
        }),
      });
      toast("Метка сохранена");
      await Promise.all([loadOverview(), openDetail(ev.event_id)]);
    } catch (err) {
      toast(`Метка не сохранена: ${err.message}`, true);
    }
  });
  form.appendChild(save);
  sec.appendChild(form);
  return sec;
}

function jsonSection(data) {
  const sec = section("JSON");
  const btn = el("button", "btn toggle-json", "Показать JSON");
  btn.type = "button";
  const pre = el("pre", "json-block", "");
  pre.hidden = true;
  btn.addEventListener("click", () => {
    pre.hidden = !pre.hidden;
    btn.textContent = pre.hidden ? "Показать JSON" : "Скрыть JSON";
    if (!pre.hidden) pre.textContent = JSON.stringify(data, null, 2);
  });
  sec.appendChild(btn);
  sec.appendChild(pre);
  return sec;
}

// ---------------------------------------------------------------- SSE

function connectSSE() {
  if (state.stream) state.stream.close();
  setStreamState("подключение…", "");
  const es = new EventSource(`/api/work/v1/stream?since=${state.lastSeq || 0}`);
  state.stream = es;
  es.onopen = () => setStreamState("SSE: онлайн", "ok");
  es.onerror = () => setStreamState("SSE: переподключение…", "bad");
  es.addEventListener("work", (e) => {
    let item;
    try { item = JSON.parse(e.data); } catch { return; }
    const seq = Number(e.lastEventId || 0);
    if (seq) state.lastSeq = Math.max(state.lastSeq, seq);
    if (!matchesFilters(item)) return;
    if (state.paused || window.scrollY > 160) {
      state.pending.push({ ev: item, seq });
      $("new-events-btn").hidden = false;
      $("new-events-btn").textContent = `${state.pending.length} новых`;
      return;
    }
    addEventRow(item, seq, true);
    $("empty-state").hidden = true;
    scheduleOverview();
  });
}

function matchesFilters(ev) {
  const f = state.filters;
  if (!f.demo && ev.demo) return false;
  if (f.session && ev.session_id !== f.session) return false;
  if (f.operation && ev.operation !== f.operation) return false;
  if (f.status && ev.status !== f.status) return false;
  if (f.checkpoint && ev.model?.checkpoint !== f.checkpoint) return false;
  if (f.error && ev.status !== "error" && ev.status !== "timeout") return false;
  if (f.fallback && ev.status !== "fallback" && !(ev.policy_result && ev.policy_result.fallback)) return false;
  if (f.truncated && !ev.truncated) return false;
  if (f.labeled && !(ev.labels && ev.labels.length)) return false;
  return true;
}

function setStreamState(text, cls) {
  const n = $("stream-state");
  n.textContent = text;
  n.className = "stream-state " + (cls || "");
}

function scheduleOverview() {
  clearTimeout(state.reloadTimer);
  state.reloadTimer = setTimeout(() => loadOverview().catch(() => {}), 800);
}

// ---------------------------------------------------------------- actions

function bind() {
  for (const id of ["f-range", "f-status"]) $(id).addEventListener("change", () => { collectFilters(); reloadAll(); });
  for (const id of ["f-session", "f-operation", "f-checkpoint"]) {
    $(id).addEventListener("change", () => { collectFilters(); reloadAll(); });
    $(id).addEventListener("keydown", (e) => { if (e.key === "Enter") { collectFilters(); reloadAll(); } });
  }
  for (const id of ["f-error", "f-fallback", "f-truncated", "f-labeled", "f-demo"]) {
    $(id).addEventListener("change", () => { collectFilters(); reloadAll(); });
  }
  $("f-reset").addEventListener("click", () => {
    state.filters = readFilters();
    state.filters = { range: "24h", session: "", operation: "", status: "", checkpoint: "", error: false, fallback: false, truncated: false, labeled: false, demo: false };
    applyFiltersToUI();
    writeFilters();
    updateExportLink();
    reloadAll();
  });
  $("refresh-btn").addEventListener("click", () => reloadAll());
  $("load-more").addEventListener("click", () => loadEvents(false).catch(err => toast(err.message, true)));
  $("pause-btn").addEventListener("click", () => {
    state.paused = !state.paused;
    $("pause-btn").textContent = state.paused ? "Продолжить" : "Пауза";
    $("pause-btn").setAttribute("aria-pressed", String(state.paused));
    if (!state.paused) flushPending();
  });
  $("new-events-btn").addEventListener("click", () => { state.paused = false; $("pause-btn").textContent = "Пауза"; $("pause-btn").setAttribute("aria-pressed", "false"); flushPending(); });
  $("close-drawer").addEventListener("click", closeDetail);
  $("drawer-overlay").addEventListener("click", closeDetail);
  document.addEventListener("keydown", (e) => { if (e.key === "Escape") closeDetail(); });
  $("copy-id").addEventListener("click", async () => {
    if (!state.detailId) return;
    try { await navigator.clipboard.writeText(state.detailId); toast("ID скопирован"); }
    catch { toast("Не удалось скопировать", true); }
  });
  $("import-file").addEventListener("change", async (e) => {
    const file = e.target.files && e.target.files[0];
    if (!file) return;
    try {
      const text = await file.text();
      const res = await api("/api/work/v1/import", {
        method: "POST",
        headers: { "Content-Type": "application/x-ndjson" },
        body: text,
      });
      toast(`Импорт: ${res.imported}, дублей ${res.duplicates}, пропущено ${res.skipped}`);
      await reloadAll();
    } catch (err) {
      toast(`Импорт не выполнен: ${err.message}`, true);
    } finally {
      e.target.value = "";
    }
  });
}

function flushPending() {
  if (!state.pending.length) return;
  for (const item of state.pending.reverse()) addEventRow(item.ev, item.seq, true);
  state.pending = [];
  $("new-events-btn").hidden = true;
  $("empty-state").hidden = true;
  scheduleOverview();
}

async function reloadAll() {
  try {
    await Promise.all([loadOverview(), loadEvents(true)]);
    connectSSE();
  } catch (err) {
    toast(`Загрузка не удалась: ${err.message}`, true);
  }
}

// ---------------------------------------------------------------- init

function init() {
  state.filters = readFilters();
  applyFiltersToUI();
  updateExportLink();
  bind();
  reloadAll();
  setInterval(() => { if (!state.paused) loadOverview().catch(() => {}); }, 15000);
}

init();

// ---------------------------------------------------------------- format

function fmtInt(v) {
  if (v === undefined || v === null) return "—";
  return Number(v).toLocaleString("ru-RU");
}

function fmtMs(v) {
  const n = Number(v);
  if (!Number.isFinite(n) || n <= 0) return "—";
  if (n < 1) return n.toFixed(2);
  if (n < 10) return n.toFixed(1);
  return Math.round(n).toLocaleString("ru-RU");
}

function fmtProb(v) {
  const n = Number(v);
  if (!Number.isFinite(n)) return "—";
  return (n * 100).toFixed(1) + "%";
}

function fmtBytes(v) {
  const n = Number(v) || 0;
  if (n < 1024) return `${n} Б`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} КБ`;
  return `${(n / (1024 * 1024)).toFixed(2)} МБ`;
}

function fmtTime(ts) {
  if (!ts) return "—";
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return String(ts);
  return d.toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

function shortSession(s) {
  if (!s) return "—";
  return String(s).replace(/^sess_/, "").slice(0, 12);
}

function inputText(ev) {
  const inp = ev.input || {};
  return String(inp.effective_redacted || inp.original_redacted || "").replace(/\s+/g, " ").trim();
}

function preview(s) {
  if (!s) return "—";
  const v = String(s);
  return v.length > 110 ? v.slice(0, 110) + "…" : v;
}
