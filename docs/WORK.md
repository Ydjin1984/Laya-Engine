# Laya Work — панель фактических решений

`http://127.0.0.1:8095/work` — встроенная в существующий Go-сервер страница,
которая показывает, что поступило в Laya, что реально ушло модели, что она
выбрала, что решил плагин, сколько это заняло и какие проверки подтвердили
результат. Отдельного inference-сервера и второго процесса нет: всё живёт в
`laya-server`.

## Что важно различать

| Понятие | Где хранится | Что означает |
|---|---|---|
| `input.original_redacted` | plugin-событие | что дал агент/хук после маскирования |
| `input.effective_redacted` | plugin/engine | что реально отправлено в `/decide` после подготовки |
| `input.received_tokens` | engine | сколько токенов состояния получил движок |
| `input.processed_tokens` | engine | сколько токенов реально увидела модель после усечения |
| `input.dropped_tokens` | engine | сколько токенов отброшено бюджетом |
| `answer_confidence` | engine | уверенность модели, **не** правильность |
| `label` | вручную | эталон для конкретного `call_id/question_id` |

Текст после токенизации/усечения не восстанавливается искусственно: панель
показывает счётчики токенов и флаг усечения, а не выдуманную строку.

## Два источника, один `call_id`

Движок пишет `kind=model_call`: фактический вход, вопросы, ответы, бюджет,
время, ошибки. Плагин пишет `kind=policy/tool_result/agent_action`: исходный
вход, действие (`ask/log/block/notify/rank/keep/check/...`), правила, evidence.
События связаны `trace_id` + `call_id`; engine- и plugin-события одного вызова
считаются **одним** решением. В ленте они склеиваются, в сводке inference
считается один раз на `call_id` и только для фактически начатого инференса
(`model_attempted=1`).

Для батчей есть `batch_id`/`item_id`; для `laya_search`/`laya_read_relevant` —
`chunk_id`, чтобы дочитывания `laya_get_chunk` прибавлялись к исходному
артефакту, а не создавали новый источник.

## Хранилище

- SQLite: `data/work.db` (по умолчанию), WAL, `busy_timeout=5000`.
- Таблицы `events` и `labels`; `PRAGMA user_version` и `meta.schema_version`,
  `meta.policy_version` фиксируют версию схемы/политики.
- Запись идёт через ограниченную очередь; ошибка БД не влияет на `/decide`.
- Retention: 30 дней, 50 000 событий и 512 МБ по умолчанию; настраивается.
- Демо-данные (`-work-demo`) помечены `demo=1`; в обычном режиме не пишутся и
  удаляются при старте без флага.

## Быстрый путь без задержек

Плагин не делает синхронный HTTP в hook. Он пишет маленький JSON в
`data/work-spool`, а сервер забирает файлы при следующем тике (250 мс). Поэтому
недоступный или зависший `/work` не добавляет задержку хукам. Если сервер
лежит, файлы ждут на диске и импортируются после старта. HTTP-приём
`POST /api/work/v1/ingest` с токеном остаётся для внешних клиентов и проверок.

## Разметка

Метка ставится на конкретный вопрос: `call_id` + `question_id` + `expected`.
Один вызов может содержать несколько вопросов с разными эталонами.

Точность считается только по размеченным вопросам:

```text
accuracy = совпадения / число размеченных вопросов
```

Рядом всегда показывается размер выборки и число неразмеченных inference-
вызовов. `npm test`/`go test` подтверждают проверку проекта, но не правильность
каждого решения Laya.

## Сокращение выдачи

Для `laya_search`/`laya_read_relevant` показываются `source_bytes`,
`returned_bytes` и `followup_bytes` (дочитывания `laya_get_chunk` по
`chunk_id`). Это **сокращение выдачи инструмента**, а не экономия сессии или
стоимости; при отсутствии данных пишется «не измерено».

## Безопасность

- `/work` и `/api/work/v1/*` принимаются только с loopback (`RemoteAddr`) и
  loopback `Host`; при `-addr` не-loopback панель отключается, а не просто
  предупреждает.
- `POST /api/work/v1/ingest` требует `X-Laya-Work-Token`; токен генерируется в
  `data/work-token`, ACL: SYSTEM/Administrators — полные, Users — чтение.
  Токен не отдаётся во frontend, не пишется в лог и не передаётся аргументом
  командной строки.
- `POST /labels` и `/import` защищены same-origin `Origin`/`Host`; чужой Origin
  получает 403.
- Секреты маскируются до записи; текст входа ограничен 64 КБ; крупные выводы
  остаются в chunk-хранилище плагина, панель показывает только `chunk_id`.
- CSP: только собственные скрипты/стили, без CDN; пользовательский текст
  выводится через `textContent`.

## API

| Метод | Путь | Назначение |
|---|---|---|
| GET | `/work` | redirect на `/work/` |
| GET | `/work/` | страница |
| GET | `/work/assets/*` | CSS/JS из `go:embed` |
| GET | `/api/work/v1/overview` | сводка, графики, accuracy, savings |
| GET | `/api/work/v1/events` | лента с фильтрами и `before_seq` |
| GET | `/api/work/v1/events/{id}` | событие, метки, связанные события |
| GET | `/api/work/v1/stream` | SSE, `Last-Event-ID`/`since` |
| POST | `/api/work/v1/ingest` | приём событий (токен) |
| POST | `/api/work/v1/labels` | ручная разметка вопроса |
| GET | `/api/work/v1/export` | JSONL/CSV с фильтрами |
| POST | `/api/work/v1/import` | импорт старых JSONL с дедупликацией |

Фильтры: `range=1h|6h|24h|7d|30d|all`, `session`, `operation`, `status`,
`checkpoint`, `error=1`, `fallback=1`, `truncated=1`, `labeled=1`, `demo=1`,
`limit`, `before_seq`.

## Флаги

| Флаг | Env | По умолчанию |
|---|---|---|
| `-work` | `LAYA_WORK` | `true` (только loopback) |
| `-work-db` | `LAYA_WORK_DB` | `data/work.db` |
| `-work-token-file` | `LAYA_WORK_TOKEN_FILE` | `data/work-token` |
| `-work-spool-dir` | `LAYA_WORK_SPOOL_DIR` | `data/work-spool` |
| `-work-demo` | `LAYA_WORK_DEMO` | `false` |
| `-work-keep-days` | `LAYA_WORK_KEEP_DAYS` | `30` |
| `-work-max-events` | `LAYA_WORK_MAX_EVENTS` | `50000` |
| `-work-max-bytes` | `LAYA_WORK_MAX_BYTES` | `536870912` |
| `-work-max-input-chars` | `LAYA_WORK_MAX_INPUT_CHARS` | `65536` |

`LAYA_WORK_TOKEN` (env) позволяет задать токен без файла; **флага токена нет
специально**, чтобы он не попадал в список аргументов процесса.

## Подключение плагина на новой машине

После установки плагина `davinci-laya-decisions@laya-zcode-stack` (0.2.2+) в его
`config.json` нужны пути к обмену с движком. Файл:

```text
%USERPROFILE%\.zcode\cli\plugins\data\davinci-laya-decisions@laya-zcode-stack\config.json
```

Добавьте блок (пути — абсолютные, подставьте каталог движка):

```json
"work": {
  "enabled": true,
  "url": "http://127.0.0.1:8095",
  "spoolDir": "E:\\Laya_Engine\\data\\work-spool",
  "tokenFile": "E:\\Laya_Engine\\data\\work-token",
  "timeoutMs": 400,
  "maxSpoolFiles": 5000
}
```

`work-spool` и `work-token` создаёт сам сервер при старте; плагин только читает
токен и пишет файлы. После правки конфига начните новую сессию ZCode: хуки и MCP
перечитывают конфиг при запуске.

## Откат

1. `-work=false` — панель и запись отключаются, `/decide` работает как раньше;
   `data/` и история сохраняются.
2. Остановить сервер, вернуть прежний `bin/laya-server.exe`, запустить.
3. Плагин: disable `0.2.x`, enable `0.1.0`; data dir не удаляется.
4. Удалять `data/work.db*` только если история больше не нужна.

## Ограничения

- У MCP-инструментов `session_id` может отсутствовать; связь идёт по
  `trace_id`, группировка — по сессии/времени.
- `laya_run_check` подтверждает проверку проекта, но не правильность решения
  Laya.
- Точность без ручной разметки не показывается.
- Текст после токенизации не восстанавливается; показываются токены и
  усечение.
- SQLite pure-Go (`modernc.org/sqlite`) увеличивает размер бинарника и время
  сборки.
