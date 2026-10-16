# Changelog

Формат: [Keep a Changelog](https://keepachangelog.com/ru/1.1.0/),
версии: [Semantic Versioning](https://semver.org/lang/ru/).

## [Unreleased]

### Planned
- Образы Docker с предустановленными весами (сейчас веса монтируются томом).
- Прогон A/B для длинных сессий (накопительный эффект экономии контекста).

## [1.0.0] — 2026-10-16

Первый публичный релиз: движок, документация на двух языках, скрипты установки
для Windows / Linux / macOS, демо-аркада и интеграция с ZCode.

### Added
- **Движок** (`engine/`, `server/`, `cmd/`, `tokenizer/`): HTTP-сервис
  типизированных решений на Go + ONNX Runtime. Типы вопросов `choice`, `score`,
  `noul`; один forward pass на батч вопросов; без генерации текста.
- **HTTP API**: `/health`, `/ready`, `/info`, `/decide`, `/count`, `/benchmark`,
  `/v1/models`, `/v1/systemone`. Контракт и примеры — `docs/API.md`.
- **Чекпоинт-идентификатор весов**: `/info` и каждый ответ несут
  `multilingual-fp16-0b095e005a4c` (sha256 весов) — пороги и замеры привязываются
  к конкретным весам.
- **Токенизатор на Go** (mmBERT: Metaspace + BPE с byte_fallback), сверен с
  HuggingFace tokenizers на 32 контрольных строках.
- **Кросс-платформенные скрипты** (`scripts/`): установка, загрузка весов и
  ONNX Runtime, загрузка CUDA 12/cuDNN 9 из колёс PyPI, сборка, запуск как
  сервиса (systemd/launchd), установка плагина ZCode.
- **Windows-скрипты**: `install.cmd`, `build.cmd`, `start-laya.cmd`,
  `stop-laya.cmd`, автозапуск через Планировщик заданий.
- **Docker**: `docker/Dockerfile` (CPU), `docker/Dockerfile.gpu` (CUDA 12 +
  cuDNN 9), `docker/docker-compose.yml`.
- **Интеграция с ZCode** (`integrations/zcode/`): плагин `davinci-laya-decisions`
  — risk-гейт на PreToolUse, ledger изменений, проверка доказательств на Stop,
  ранжирование контекста и 10 MCP-инструментов. Отдельный репозиторий:
  https://github.com/Ydjin1984/davinci-laya-zcode
- **Документация**: двуязычный README (RU/EN), `docs/INSTALL.md`,
  `docs/API.md`, `docs/BENCHMARKS.md`, `docs/ARCHITECTURE.md`,
  `docs/ENGINE.ru.md`, `docs/TZ-ZCode-Laya-Integration.md`.
- **Демо** `laya_arcade/`: три игры, в которые играет Laya, и нейроэволюция
  «учеников» рядом.
- **CI** (GitHub Actions): сборка и тесты на Ubuntu, Windows и macOS.
- **Иллюстрации** (`01_laya_engine.png` … `04_practical_use.png`) — единая серия
  инфографики для быстрого введения (иллюстрация 03 — три сценария экономии:
  2 800 000 → 992 037 → 513 614 с явными оговорками к −81,7 %/−48 %); `05_gpu_gtx1650.png` — Диспетчер задач
  тестовой машины (движок на GTX 1650: 2,1 из 4,0 ГБ видеопамяти, 3D 9 %, 49 °C)
  как доказательство реального локального запуска.

### Fixed (переносимость)
- `engine/cudaenv.go`: разделитель списка путей теперь системный (`;` на Windows,
  `:` на Linux/macOS) — раньше на Unix-системах PATH процесса мог быть испорчен.
  Добавлен поиск библиотек CUDA в Linux/macOS-раскладке пакетов `nvidia-*`
  (`.../nvidia/<pkg>/lib/`).

### Known limitations
- `bin/laya-hook.exe` плагина (быстрый лаунчер хуков) — Windows-бинарь; на
  Linux/macOS он собирается из исходника одной командой `go build` (см.
  `scripts/install-plugin.sh`).
- Роутер задач у плагина — совещательный сигнал (~58% качества), автопереключение
  модели не поддерживается.
- CUDA Graph на связке ORT 1.25 / CUDA 12 падает (illegal memory access) —
  не включать.
