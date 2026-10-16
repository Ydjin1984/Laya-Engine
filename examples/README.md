# Примеры вызова API / API examples

Минимальные клиенты к локальному сервису (`127.0.0.1:8095`). Все примеры
предполагают, что движок запущен:

```bash
./scripts/laya.sh start        # Linux/macOS
start-laya.cmd                 # Windows
```

| Файл | Язык | Запуск |
|---|---|---|
| `curl-examples.sh` | bash + curl | `bash examples/curl-examples.sh` |
| `powershell-example.ps1` | Windows PowerShell 5.1+ | `powershell -ExecutionPolicy Bypass -File examples\powershell-example.ps1` |
| `python_client.py` | Python 3 (только stdlib) | `python examples/python_client.py` |
| `node_client.mjs` | Node 18+ | `node examples/node_client.mjs` |

Все примеры проверены на живом сервисе (чекпоинт
`multilingual-fp16-0b095e005a4c`): маршрут `files_write` (0.985), риск `0.95`
(высокий), `data_loss` = 0.912. Во всех примерах — один и тот же сценарий:

1. `/health` и `/ready` — процесс жив, модель загружена;
2. `/count` — бюджет состояния **без инференса** (полезно до вызова модели);
3. `/decide` — три типа вопросов в одном запросе: `choice`, `score`, `noul`;
4. разбор ответа: выбранный вариант, вероятности, `answer_confidence`,
   чекпоинт, бюджеты и признаки обрезки (`state_truncated`).

> **Формулировка вопроса влияет на ответ.** Типы `noul`/`score` у модели на
> 322M параметра чувствительны к формулировке: конкретное утверждение
> («уничтожит ли это действие невосстановимые данные…») даёт осмысленный ответ
> там, где короткий абстрактный вопрос («изменяет ли запрос состояние?») даёт
> ложный ноль. Правила и проверенные формулировки — в
> [../docs/API.md](../docs/API.md#формулировки-вопросов).

Документация контракта — [../docs/API.md](../docs/API.md).
