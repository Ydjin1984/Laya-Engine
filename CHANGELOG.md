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

### Fixed (сверка с эталоном)
- Тяжёлый тест `TestDecideMatchesPythonReference` больше не подставляет
  `tools/ref_answers.json` (эталон снят на **испорченной int8-модели**): эталон
  выбирается по устройству (`ref_answers_gpu.json` / `ref_answers_fp16.json`),
  и сверка пропускается, если файл снят на других весах. На исправном пути:
  1200 ответов, 0 расхождений; один почти-равный выбор («BTC», разрыв лидеров
  0.0004) на другой видеокарте переворачивается и печатается отдельной строкой.
- `engine/cudaenv.go`: каталог `third_party/cuda12` теперь ищется и вверх по
  дереву от рабочего каталога — `go test ./engine/` больше не требует
  `LAYA_CUDA_DLL_DIRS`.

### Fixed (переносимость)
- `engine/cudaenv.go`: разделитель списка путей теперь системный (`;` на Windows,
  `:` на Linux/macOS) — раньше на Unix-системах PATH процесса мог быть испорчен.
  Добавлен поиск библиотек CUDA в Linux/macOS-раскладке пакетов `nvidia-*`
  (`.../nvidia/<pkg>/lib/`).

### Fixed (инфраструктура релиза)
- `release.yml`: шаг прикрепления бинарников к релизу падал с `not a git
  repository` (в job'е нет checkout'а) — добавлен `GH_REPO`; ручной запуск
  (`workflow_dispatch`) теперь принимает вход `tag`, потому что файл workflow на
  теге заморожен в его коммите. Историю прогонов видно в Actions: два прогона
  «Release binaries» (#1, #2) упали именно на этом шаге — сборка всех пяти
  бинарников в них прошла успешно. Тег `v1.0.0` переставлен на коммит с
  исправленным workflow (`git push -f origin v1.0.0`), после чего прогон по тегу
  завершился успешно и бинарники прикреплены к релизу.

### Known limitations
- Бинарники из CI не бит-воспроизводимы между прогонами: сборка идёт с cgo, а
  тулчейн раннера (gcc, версия Go «stable») меняется между запусками. Размеры и
  хеши двух пересборок одного тега отличаются на байты — сверяйте бинарники с
  выводом конкретного job'а, а не с прошлым релизом.
- `bin/laya-hook.exe` плагина (быстрый лаунчер хуков) — Windows-бинарь; на
  Linux/macOS он собирается из исходника одной командой `go build` (см.
  `scripts/install-plugin.sh`).
- Роутер задач у плагина — совещательный сигнал (~58% качества), автопереключение
  модели не поддерживается.
- CUDA Graph на связке ORT 1.25 / CUDA 12 падает (illegal memory access) —
  не включать.
