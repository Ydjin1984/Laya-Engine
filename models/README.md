# models/ — веса и словарь

Эти файлы **не хранятся в git** (617 МБ + 33 МБ): их скачивают скрипты установки
и проверяют по SHA-256. Манифест `model-manifest.json` — источник правды по
размерам и контрольным суммам.

| Файл | Размер | Роль | Источник |
|---|---|---|---|
| `multilingual-fp16.onnx` | 646 870 871 Б | рабочие веса fp16 (322M параметров) | [mizchi/laya-multilingual-onnx](https://huggingface.co/mizchi/laya-multilingual-onnx) |
| `tokenizer.json` | 34 363 188 Б | словарь mmBERT, 256 000 токенов | [techtheist/laya-onnx](https://huggingface.co/techtheist/laya-onnx) → `multilingual/tokenizer.json` |
| `model-manifest.json` | — | SHA-256 и размеры (в репозитории) | — |
| `rl_agent_config.json` | — | параметры чекпоинта: `max_len=1024`, `head_max_len=256`, температуры | — |
| `multilingual-int8.onnx` | 873 МБ | **не использовать** (квантование испортило голову решений) | — |

## Как получить

```bash
./scripts/fetch-model.sh            # Linux/macOS: скачать + проверить SHA-256
powershell tools\fetch-model.ps1    # Windows
```

Ручное скачивание (если скрипты недоступны):

```bash
curl -L -o models/multilingual-fp16.onnx \
  https://huggingface.co/mizchi/laya-multilingual-onnx/resolve/main/model.onnx
curl -L -o models/tokenizer.json \
  https://huggingface.co/techtheist/laya-onnx/resolve/main/multilingual/tokenizer.json
```

Контрольный SHA-256 рабочей модели:
`0b095e005a4c295cae74d47b7eb6931c369d48f5b720b45278d774165798310c`.
Именно этот хеш попадает в `/info` как короткий id чекпоинта
`multilingual-fp16-0b095e005a4c` — клиенты обязаны привязывать свои пороги и
замеры к нему: подмена весов без смены id невозможна.

## Почему INT8-экспорт запрещён

У `multilingual-int8.onnx` квантование уничтожило голову решений: логиты
вариантов почти совпадают, `act_logits` выходят за 1000. Симптом — уверенность
≈ 1/k (равномерное распределение) у всех вопросов и случайные ответы на
эталонном примере. Причина: голова (`scorer`, `act_head`), LayerNorm и
эмбеддинги квантованию не подлежат. Подробнее — [../docs/ENGINE.ru.md](../docs/ENGINE.ru.md).
