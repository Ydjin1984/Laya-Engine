# HTTP API

Двуязычный справочник: русская версия, затем английская
([English version below](#http-api-english)).

Сервис слушает только loopback: `127.0.0.1:8095` по умолчанию (`-addr` /
`LAYA_ADDR`). Аутентификации нет — это локальный сервис.

---

# 🇷🇺 HTTP API (русский)

## Эндпоинты

| Метод | Путь | Назначение |
|---|---|---|
| GET | `/health` | процесс жив (модель может ещё грузиться) |
| GET | `/ready` | модель загружена и готова отвечать |
| GET | `/info` | версия, устройство, модель, **чекпоинт**, счётчики, лимиты |
| POST | `/decide` | ответы на батч вопросов по одному состоянию |
| POST | `/count` | бюджеты состояния и вопросов **без инференса** (токенизация, ~1 мс) |
| POST | `/benchmark` | замер p50/p95/p99 и пропускной способности |
| GET | `/v1/models` | Jev-совместимый список модели (id `laya-go`, чекпоинт, лимиты) |
| POST | `/v1/systemone` | Jev-совместимая форма ответа (та же логика, что `/decide`) |

Родной контракт — `/decide`: он полнее (порядок вопросов, бюджеты, телеметрия
на каждый вопрос).

## Типы вопросов

| Тип | Что отвечает | Поля ответа |
|---|---|---|
| `choice` | выбор одного из вариантов `criteria` | `choice`, `probabilities` |
| `score` | ожидаемый уровень по упорядоченной шкале `criteria` (от низшего к высшему) | `score`, `probabilities` |
| `noul` | P(утверждение верно) — бинарная оценка | `noul` |

Порядок вероятностей совпадает с порядком вариантов в запросе. Порядок ключей в
объекте `answers` в JSON **не гарантирован** — сопоставляйте по массиву `order`.

## POST /decide

Запрос:

```json
{
  "state": "удали файл D:\\tmp\\old.txt и покажи список",
  "questions": {
    "route": {
      "type": "choice",
      "instructions": "Какой набор инструментов нужен?",
      "criteria": {
        "dialog": "просто ответить словами",
        "files_write": "изменить или удалить файл на диске",
        "console": "выполнить команду в терминале"
      }
    },
    "risk": {
      "type": "score",
      "instructions": "Оцени риск действия по шкале.",
      "criteria": ["низкий", "средний", "высокий"]
    },
    "data_loss": {
      "type": "noul",
      "instructions": "Уничтожит ли это действие несохранённые или невосстановимые данные пользователя (сброс изменений, удаление файлов, перезапись без копии)?"
    }
  },
  "max_len": 1024
}
```

Ответ (проверено на живом сервисе, чекпоинт `multilingual-fp16-0b095e005a4c`):

```json
{
  "version": 1,
  "model": "laya-go",
  "checkpoint": "multilingual-fp16-0b095e005a4c",
  "answers": {
    "route": {
      "type": "choice", "choice": "files_write",
      "probabilities": [
        {"label": "dialog", "value": 0.009},
        {"label": "files_write", "value": 0.985},
        {"label": "console", "value": 0.006}
      ],
      "confidence": 0.94, "answer_confidence": 0.985,
      "act_probability": 1, "temperature": 1, "state_dropped": 0
    },
    "risk":     {"type": "score", "score": 0.95,  "answer_confidence": 0.797, "...": "..."},
    "data_loss": {"type": "noul", "noul": 0.912, "answer_confidence": 0.912, "...": "..."}
  },
  "order": ["route", "risk", "data_loss"],
  "usage": {"input_tokens": 176, "output_tokens": 0},
  "budget": {
    "state_tokens": 14,
    "state_dropped_max": 0,
    "state_truncated": false,
    "max_len": 1024
  },
  "elapsed_ms": 234.8
}
```

`max_len` (64..8192) — необязательное переопределение бюджета на один запрос.
Один запрос — один forward pass: все вопросы считаются батчем, поэтому «цена»
запроса определяется числом вопросов и длиной состояния, а не параллелизмом.

## POST /count

Тот же вход, что у `/decide`, но модель **не вызывается** — ответ только про
бюджеты (удобно, чтобы укоротить состояние до инференса):

```json
{
  "state_tokens": 14, "max_len": 1024, "head_max_len": 256, "fits": true,
  "questions": [{
    "options": 3, "options_distinct": 3, "collapsed": false,
    "head_tokens": 12, "head_truncated": false, "option_tokens": 24,
    "state_kept": 14, "state_dropped": 0, "seq_len": 62, "fits": true
  }],
  "elapsed_ms": 1.24
}
```

## POST /benchmark

```json
{"state": "разбери BTC по Герчику", "questions": { "...": {} }, "iterations": 30, "concurrency": 1}
```

Ответ содержит p50/p95/p99, среднее и пропускную способность. Замеряется на
вашей машине: числа зависят от GPU (см. [BENCHMARKS.md](BENCHMARKS.md)).

## Семантика полей ответа

**`answer_confidence` = max(p).** Именно эту величину калибрует температурное
масштабирование — **порог ставьте по ней**.

**`confidence`** — нормированная энтропия (1 − H/log k). Не калибрована;
сравнивать её с тем же порогом нельзя.

**`act_probability`** — softmax от `act_logits`. У этого чекпоинта значения
насыщены (почти всегда 1.0), потому что `act_logits` выходят за ±1000. Как
совещательный сигнал бесполезна — не полагайтесь на неё.

**Обрезка состояния.** Состояние режется **справа** под бюджет `max_len`; у
разных вопросов разное число вариантов, поэтому свободного места под состояние
разное. Если `budget.state_truncated = true` или `state_dropped > 0` — модель не
видела хвост текста, и «уверенный» ответ может быть уверенным именно поэтому.
**Решающие факты кладите в НАЧАЛО состояния.** `head_truncated` — не поместилась
инструкция вопроса; `collapsed` — варианты после обрезки стали неразличимы
(ответ недостоверен). Клиент обязан учитывать эти флаги так же строго, как и
сам ответ.

**Чекпоинт.** `/info` и каждый ответ `/decide` несут идентификатор весов:

```json
"checkpoint": {
  "file": "multilingual-fp16.onnx",
  "bytes": 646870871,
  "sha256": "0b095e005a4c295cae74d47b7eb6931c369d48f5b720b45278d774165798310c",
  "source": "manifest"
}
```

Короткий id для логов — `multilingual-fp16-0b095e005a4c`. **Привязывайте пороги
и замеры к этому id**: подмена весов без смены id невозможна, а пороги,
откалиброванные на одних весах, на других недействительны.

## Формулировки вопросов

Модель небольшая (322M), и качество ответа зависит от формулировки. Правила,
проверенные на этом чекпоинте:

1. **Один вопрос — одно решение.** Не объединяйте «ошибка ли это и насколько
   опасно» в один вопрос.
2. **Один вызов — все вопросы.** Независимые вопросы — в один батч: один
   forward pass вместо N.
3. **Для `noul` пишите полное утверждение, а не абстрактный вопрос.**
   Измерено на состоянии «удали файл D:\tmp\old.txt…»:

   | Формулировка | `noul` | Итог |
   |---|---|---|
   | «Изменяет ли запрос состояние?» | 0.002 | ложный ноль |
   | «Удаляет ли запрос файлы?» | 0.000 | ложный ноль |
   | «Уничтожит ли это действие несохранённые или невосстановимые данные пользователя (сброс изменений, удаление файлов, перезапись без копии)?» | **0.912** | осмысленно |

   Конкретное утверждение о действии с раскрытыми скобками работает; короткий
   вопрос про «запрос» — нет.
4. **Для `score` перечисляйте уровни от низшего к высшему и называйте измерение**
   («Оцени риск действия по шкале» + `["низкий","средний","высокий"]` даёт 0.95 —
   «высокий»; нейтральное «Насколько действие опасно?» давало размытые 0.46).
5. **Прогоняйте `/count` перед сложными запросами** — дешевле (без инференса)
   узнать, влезает ли состояние.
6. **Калибруйте на своих данных.** Пороги `answer_confidence` из чужих замеров —
   только стартовая точка; методика — `CALIBRATION.md` в репозитории плагина.

---

# 🇬🇧 HTTP API (English)

Loopback-only service (`127.0.0.1:8095` by default, `-addr` / `LAYA_ADDR`); no
authentication — it is a local service.

## Endpoints

| Method | Path | Purpose |
|---|---|---|
| GET | `/health` | process alive (the model may still be loading) |
| GET | `/ready` | model loaded and answering |
| GET | `/info` | version, device, model, **checkpoint**, counters, limits |
| POST | `/decide` | answers to a batch of questions over one state |
| POST | `/count` | budgets only, **no inference** (tokenization, ~1 ms) |
| POST | `/benchmark` | p50/p95/p99 and throughput on this machine |
| GET | `/v1/models` | Jev-compatible model list (id `laya-go`) |
| POST | `/v1/systemone` | Jev-compatible response shape (same logic as `/decide`) |

Question types: `choice` (pick one of `criteria`), `score` (expected level on an
ordered scale, low → high), `noul` (P(statement is true)). Probability order
matches the order of the criteria; `answers` object key order is **not**
guaranteed — use `order`.

## Reading the answer correctly

- **`answer_confidence` = max(p)** — the calibrated value; set your thresholds on
  it. `confidence` (entropy) is not calibrated; `act_probability` is saturated on
  this checkpoint and useless as a signal.
- **Truncation:** the state is cut from the right; if `budget.state_truncated` or
  `state_dropped > 0`, the model never saw the tail — put decision-critical facts
  first. `collapsed` means the options became indistinguishable (unreliable).
- **Checkpoint:** every answer carries the weights id
  (`multilingual-fp16-0b095e005a4c`). Bind thresholds and measurements to it.

## Wording rules (measured on this checkpoint)

1. One question — one decision; independent questions go into ONE batch call.
2. `noul` wants a **full, concrete statement**, not an abstract query: on the
   state "delete file D:\tmp\old.txt" the phrasing "Does the request modify
   state?" returned 0.002 (false zero), "Does it delete files?" returned 0.000,
   while "Will this action destroy unrecoverable user data (reset, deletion,
   overwrite without a copy)?" returned **0.912**.
3. `score` likes explicit levels low → high plus a named dimension
   ("rate the risk" + low/medium/high → 0.95 high vs a vague 0.46).
4. Run `/count` first when the state may not fit; it costs tokenization only.
5. Calibrate thresholds on your own data; foreign numbers are just a starting
   point.
