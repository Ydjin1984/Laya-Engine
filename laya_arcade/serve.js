// serve.js — сервер аркады: страница, прокси к экземплярам Laya и СЕРВЕРНОЕ обучение.
//
// Всё, что раньше жило в браузере (популяции, поколения, рекорды), теперь работает
// в этом процессе: обучение идёт даже при закрытой вкладке, состояние сохраняется
// на диск (state/trainer-state.json) и восстанавливается после перезапуска сервера.
import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import url from 'node:url';
import { Trainer } from './server/trainer.mjs';
const ROOT = path.dirname(url.fileURLToPath(import.meta.url));
const PORT = Number(process.env.PORT || 8099);
const STATE_FILE = path.join(ROOT, 'state', 'trainer-state.json');

/* Лог в файл. Сервер запускается скриптом в отдельном свёрнутом окне: без файла
   причина падения (занятый порт, ошибка импорта) исчезает вместе с окном. */
const LOG_FILE = process.env.LAYA_ARCADE_LOG || path.join(ROOT, 'logs', 'arcade.log');
try {
  fs.mkdirSync(path.dirname(LOG_FILE), { recursive: true });
  const stream = fs.createWriteStream(LOG_FILE, { flags: 'a' });
  const tee = (write) => (...args) => {
    write(...args);
    try { stream.write(`${new Date().toISOString()} ${args.map((a) => (typeof a === 'string' ? a : String(a))).join(' ')}\n`); } catch { /* лог не критичен */ }
  };
  console.log = tee(console.log.bind(console));
  console.error = tee(console.error.bind(console));
} catch {
  /* нет доступа к каталогу логов — работаем только в консоль */
}

const trainer = new Trainer({ stateFile: STATE_FILE, autosaveMs: 15000, budgetMs: 10 });

const TYPES = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.ico': 'image/x-icon',
};

// Постоянные соединения к экземплярам Laya: снимает накладные на каждый опрос модели.
const layaAgent = new http.Agent({ keepAlive: true, maxSockets: 32 });

const json = (res, code, obj) => {
  res.writeHead(code, { 'content-type': 'application/json; charset=utf-8', 'cache-control': 'no-store', 'access-control-allow-origin': '*' });
  res.end(JSON.stringify(obj));
};

const readBody = (req) => new Promise((resolve) => {
  const chunks = [];
  req.on('data', (c) => chunks.push(c));
  req.on('end', () => {
    const raw = Buffer.concat(chunks).toString('utf8');
    if (!raw) return resolve({});
    try { resolve(JSON.parse(raw)); } catch { resolve({}); }
  });
});

const server = http.createServer(async (req, res) => {
  const parsed = new URL(req.url, 'http://127.0.0.1').pathname;

  /* ---------------- API серверного обучения ---------------- */
  if (parsed === '/api/snapshot') {
    return json(res, 200, trainer.snapshot());
  }

  if (parsed === '/api/brain') {
    return json(res, 200, trainer.exportBrain());
  }

  if (parsed === '/api/state') {
    try {
      return json(res, 200, JSON.parse(fs.readFileSync(STATE_FILE, 'utf8')));
    } catch (e) {
      return json(res, 404, { error: 'состояние ещё не сохранено', detail: String(e.message) });
    }
  }

  if (parsed === '/api/cmd' && req.method === 'POST') {
    const body = await readBody(req);
    const cmd = body.cmd;
    try {
      switch (cmd) {
        case 'pause': trainer.setPaused(true); break;
        case 'resume': trainer.setPaused(false); break;
        case 'budget': trainer.setBudget(body.value); break;
        case 'focus': trainer.setFocus(body.value); break;
        case 'reset': trainer.reset(); break;
        case 'load': trainer.loadNets(body.nets || {}, body.scores || {}); break;
        case 'save': trainer.saveState(); break;
        default: return json(res, 400, { error: `неизвестная команда: ${cmd}` });
      }
      return json(res, 200, { ok: true, cmd, paused: trainer.paused, budgetMs: trainer.budgetMs, focusMode: trainer.focusMode });
    } catch (e) {
      return json(res, 500, { error: String(e.message) });
    }
  }

  // Рекорды Лая-игроков: сохраняются на сервере, чтобы не пропадали при обновлении страницы
  if (parsed === '/api/players' && req.method === 'POST') {
    const body = await readBody(req);
    trainer.mergePlayers(body);
    return json(res, 200, { ok: true, players: trainer.players });
  }

  /* ---------------- прокси к экземплярам Laya ---------------- */
  // /laya/8101/decide → http://127.0.0.1:8101/decide
  if (parsed.startsWith('/laya/')) {
    const m = parsed.match(/^\/laya\/(\d{2,5})(\/.*)$/);
    if (!m) { res.writeHead(400).end('bad laya path'); return; }
    const port = Number(m[1]);
    const target = m[2];
    const chunks = [];
    req.on('data', (c) => chunks.push(c));
    req.on('end', () => {
      const body = Buffer.concat(chunks);
      const preq = http.request({
        host: '127.0.0.1', port, path: target, method: req.method, agent: layaAgent,
        headers: { 'content-type': req.headers['content-type'] || 'application/json', 'content-length': body.length },
        timeout: 30000,
      }, (pres) => {
        const out = [];
        pres.on('data', (c) => out.push(c));
        pres.on('end', () => {
          res.writeHead(pres.statusCode, {
            'content-type': pres.headers['content-type'] || 'application/json',
            'access-control-allow-origin': '*',
            'cache-control': 'no-store',
          });
          res.end(Buffer.concat(out));
        });
      });
      preq.on('error', (e) => { res.writeHead(502, { 'content-type': 'application/json' }); res.end(JSON.stringify({ error: String(e.message) })); });
      preq.on('timeout', () => { preq.destroy(); res.writeHead(504, { 'content-type': 'application/json' }); res.end(JSON.stringify({ error: 'timeout' })); });
      if (body.length) preq.write(body);
      preq.end();
    });
    return;
  }

  /* ---------------- статика ---------------- */
  let rel = decodeURIComponent(parsed);
  if (rel === '/') rel = '/index.html';
  const filePath = path.join(ROOT, path.normalize(rel).replace(/^([/\\])+/, ''));

  if (!filePath.startsWith(ROOT)) {
    res.writeHead(403).end('forbidden');
    return;
  }

  fs.readFile(filePath, (err, data) => {
    if (err) {
      res.writeHead(404, { 'content-type': 'text/plain; charset=utf-8' });
      res.end('404: ' + rel);
      return;
    }
    res.writeHead(200, {
      'content-type': TYPES[path.extname(filePath).toLowerCase()] || 'application/octet-stream',
      'cache-control': 'no-store',
      'access-control-allow-origin': '*',
    });
    res.end(data);
  });
});

server.listen(PORT, '127.0.0.1', () => {
  console.log(`Лай Аркада: http://127.0.0.1:${PORT}/`);
  console.log(`Обучение идёт на сервере; состояние: ${STATE_FILE}`);
  console.log(`Лог: ${LOG_FILE}`);
});

// Ошибка прослушивания не должна выглядеть как «бэкенд не отвечает»: занятый
// порт означает, что сервер уже запущен, и это надо сказать прямо.
server.on('error', (e) => {
  if (e.code === 'EADDRINUSE') {
    console.error(`[сервер] порт ${PORT} уже занят — сервер аркады, скорее всего, уже работает: http://127.0.0.1:${PORT}/`);
  } else {
    console.error(`[сервер] не удалось запуститься: ${e.message}`);
  }
  process.exit(2);
});

const shutdown = () => {
  console.log('[сервер] остановка, сохраняю состояние обучения…');
  trainer.stop();
  server.close(() => process.exit(0));
  setTimeout(() => process.exit(0), 1500);
};
process.on('SIGINT', shutdown);
process.on('SIGTERM', shutdown);
