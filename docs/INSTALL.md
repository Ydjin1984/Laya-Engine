# Установка / Installation

Двуязычный документ: русский текст, затем английская версия
([English version below](#installation-english)).

---

# 🇷🇺 Установка (русский)

## Что будет установлено

| Компонент | Размер | Откуда |
|---|---|---|
| Веса модели `multilingual-fp16.onnx` | 617 МБ | HuggingFace (`mizchi/laya-multilingual-onnx`), проверка SHA-256 |
| Словарь `tokenizer.json` | 33 МБ | HuggingFace (`techtheist/laya-onnx`) |
| ONNX Runtime 1.25.0 | 80–300 МБ | официальный релиз Microsoft |
| CUDA 12 + cuDNN 9 (только GPU) | ≈1,5 ГБ | колёса PyPI `nvidia-*-cu12` |
| Бинарник `laya-server` | 13 МБ | сборка из исходников или релизный ассет |

Итого: ≈750 МБ для CPU-установки, ≈2,3 ГБ для GPU-установки. Всё скачивается
**один раз** при установке; в рантайме сервис в сеть не ходит.

## Требования по платформам

| Платформа | Запуск | Сборка из исходников | GPU |
|---|---|---|---|
| Windows 10/11 x64 | ✅ | Go ≥ 1.26 + gcc (MSYS2) | CUDA-драйвер ≥ 525 |
| Linux x64 | ✅ | Go ≥ 1.26 + gcc | CUDA-драйвер ≥ 525 |
| Linux arm64 | ✅ | Go ≥ 1.26 + gcc | нет GPU-сборки ORT (CPU) |
| macOS (Intel/Apple Silicon) | ✅ | Go ≥ 1.26 + clang (Xcode CLT) | нет (CUDA недоступна, CPU) |
| Docker (любой хост) | ✅ | не требуется | nvidia-container-toolkit |

Рантайм Python **не нужен** (токенизатор и сборка входа — на Go).

## Windows

```cmd
install.cmd                  :: GPU-установка (без NVIDIA-GPU автоматически станет CPU)
install.cmd -Cpu             :: CPU-only: без скачивания CUDA
install.cmd -Prebuilt        :: без Go/gcc — скачать готовый бинарник
install.cmd -Service         :: + автозапуск при загрузке (запускать от администратора)
install.cmd -Plugin          :: + плагин и навык ZCode
```

Ручной путь (если скрипты не подходят):

```cmd
powershell -ExecutionPolicy Bypass -File tools\fetch-model.ps1        :: веса + словарь
powershell -ExecutionPolicy Bypass -File scripts\windows\fetch-ort.ps1 :: ONNX Runtime
powershell -ExecutionPolicy Bypass -File scripts\windows\fetch-cuda.ps1 :: CUDA 12 / cuDNN 9 (GPU)
build.cmd                                                             :: gofmt + vet + test + сборка
start-laya.cmd                                                        :: запуск (порт 8095)
start-laya.cmd /cpu                                                   :: запуск на CPU
stop-laya.cmd                                                         :: остановка
install-autostart.cmd                                                 :: автозапуск при загрузке (админ)
```

- **Автозапуск**: задача Планировщика `LayaEngineServer`, учётная запись SYSTEM,
  триггер при загрузке + 30 с (драйвер NVIDIA успевает подняться), перезапуск
  3 раза при сбое. Проверка: `schtasks /query /tn LayaEngineServer /v /fo list`.
- **Сторож**: `scripts\windows\watchdog\setup-watchdog.ps1` (от админа) — каждые
  5 минут проверяет порт и поднимает задачу, если сервис молчит (например,
  после сброса драйвера). Лог: `logs\watchdog.log`.
- **Проверка установки**: `bin\laya-server.exe -device cuda -device-id 0
  -model models\multilingual-fp16.onnx -ort bin\onnxruntime.dll -selftest`.

## Linux

```bash
./scripts/install.sh                 # GPU (если есть nvidia-smi), сборка из исходников
./scripts/install.sh --cpu           # CPU-only
./scripts/install.sh --prebuilt      # готовый бинарник из релиза (Go не нужен)
./scripts/install.sh --service       # systemd user-юнит (автозапуск при входе)
./scripts/install.sh --plugin        # плагин и навык ZCode
```

Ручной путь:

```bash
./scripts/fetch-model.sh             # веса + словарь (SHA-256)
./scripts/fetch-ort.sh --gpu         # ONNX Runtime (linux-x64-gpu)
./scripts/fetch-cuda.sh              # CUDA 12 / cuDNN 9 из колёс PyPI (~1.5 GB)
./scripts/build.sh                   # gofmt + vet + test + сборка
./scripts/laya.sh start              # запуск (foreground-режим — laya.sh run)
./scripts/laya.sh status             # /ready, /info, pid, хвост лога
./scripts/laya.sh logs               # tail -f
./scripts/laya.sh stop
```

- **Автозапуск**: `systemctl --user status laya-engine`, выживает без логина при
  `loginctl enable-linger $USER`. Юнит — `~/.config/systemd/user/laya-engine.service`.
- **CUDA уже стоит?** Если вы ставили `pip install nvidia-cublas-cu12` и т. п.,
  `fetch-cuda.sh` можно пропустить: движок сам находит пакеты `nvidia-*`
  (Linux site-packages), либо укажите каталоги явно:
  `LAYA_CUDA_DLL_DIRS=/path/lib1:/path/lib2`.
- Требования сборки: Debian/Ubuntu `build-essential`, Fedora
  `@development-tools`, Arch `base-devel`.

## macOS

```bash
xcode-select --install               # clang для cgo
./scripts/install.sh --cpu           # CUDA на macOS нет — всегда CPU
./scripts/install.sh --cpu --service # + launchd-агент (автозапуск при входе)
```

Автозапуск: `~/Library/LaunchAgents/com.davinci.laya-engine.plist`
(`launchctl list | grep davinci`). Управление: `launchctl load/unload <plist>`.

## Docker

```bash
./scripts/fetch-model.sh                       # веса один раз, на хосте
docker compose up --build laya-cpu             # CPU-образ
COMPOSE_PROFILES=gpu docker compose up --build laya-gpu   # GPU (linux + nvidia-container-toolkit)
```

Подробности — [../docker/README.md](../docker/README.md).

## Проверка после установки

```bash
curl http://127.0.0.1:8095/health    # {"ok":true,...}
curl http://127.0.0.1:8095/ready     # {"ready":true}
curl http://127.0.0.1:8095/info      # device: cuda|cpu, checkpoint, лимиты

python examples/python_client.py     # end-to-end: count -> decide -> разбор
node   examples/node_client.mjs
bash   examples/curl-examples.sh
```

Ожидаемый ответ проверочного сценария (чекпоинт
`multilingual-fp16-0b095e005a4c`): `route=files_write` (≈0.99),
`risk≈0.95` (высокий), `data_loss≈0.91`.

## Обновление и удаление

```bash
git pull                    # обновить код
./scripts/build.sh          # пересобрать (Windows: build.cmd — при запущенном
                            # сервисе сборка уйдёт в bin\laya-server.new.exe)
```

Удаление: остановить сервис (`./scripts/laya.sh stop` / `stop-laya.cmd`), снять
автозапуск (`--service`-юнит / `uninstall-autostart.cmd` / `launchctl unload`),
удалить каталог репозитория. Веса и библиотеки лежат внутри него — отдельного
«следа» в системе нет.

## Диагностика

| Симптом | Причина и что делать |
|---|---|
| `CUDA недоступна ... cublasLt64_12.dll missing` | нет библиотек CUDA 12 → `fetch-cuda.*`, либо `--cpu` |
| `CUDA provider ... libcudnn.so.9: cannot open` (Linux) | то же; проверьте, что `third_party/cuda12` в `LD_LIBRARY_PATH` (это делает `laya.sh`) |
| `Allocator ... failed` / OOM | карта занята: `nvidia-smi`; снизьте `-max-len` |
| Первый вызов ~500 мс, дальше 50 мс | не отработал прогрев (`-warmup`); при долгом простое повторится |
| У всех вопросов уверенность ≈ 1/k | взята INT8-модель — см. [../models/README.md](../models/README.md) |
| `unsupported_schema` | версия API сервиса и клиента разошлись |
| Сервис отвечает, `/ready` не готов | веса грузятся (5–10 с CPU, быстрее GPU) |
| Python-клиент: `RemoteDisconnected` | системный HTTP-прокси перехватывает loopback — пример уже обходит прокси, посмотрите `ProxyHandler` в `examples/python_client.py` |
| Порт занят другим процессом | `stop-laya.cmd` откажется убивать чужой процесс: разбирайтесь вручную |

## Загрузка без скриптов (полностью вручную)

1. Веса:
   `curl -L -o models/multilingual-fp16.onnx https://huggingface.co/mizchi/laya-multilingual-onnx/resolve/main/model.onnx`
2. Словарь:
   `curl -L -o models/tokenizer.json https://huggingface.co/techtheist/laya-onnx/resolve/main/multilingual/tokenizer.json`
3. SHA-256 рабочей модели должен быть равен
   `0b095e005a4c295cae74d47b7eb6931c369d48f5b720b45278d774165798310c`.
4. ONNX Runtime 1.25.0 — файлы из
   `https://github.com/microsoft/onnxruntime/releases/tag/v1.25.0`:
   `onnxruntime.dll` → `bin\` (Windows), `libonnxruntime.so*` → `bin/` (Linux).
5. CUDA 12/cuDNN 9 (GPU): `pip download nvidia-cublas-cu12 nvidia-cudnn-cu12
   nvidia-cufft-cu12 nvidia-cuda-runtime-cu12 --no-deps -d wheels` → распаковать
   и положить библиотеки в `third_party\cuda12\` (`third_party/cuda12/`).
6. Собрать: `build.cmd` (Windows) или `./scripts/build.sh` (Linux/macOS), либо
   взять бинарник из релиза.

---

# 🇬🇧 Installation (English)

## What gets installed

Weights (617 MB) and tokenizer (33 MB) from HuggingFace with SHA-256
verification, ONNX Runtime 1.25.0 from Microsoft's official release, CUDA 12 +
cuDNN 9 wheels from PyPI (GPU only, ≈1.5 GB), and the `laya-server` binary
(13 MB). Total: ≈750 MB for a CPU install, ≈2.3 GB for GPU. Everything is
downloaded **once**, at install time — the service never touches the network at
runtime.

## Platform matrix

| Platform | Run | Build from source | GPU |
|---|---|---|---|
| Windows 10/11 x64 | ✅ | Go ≥ 1.26 + gcc (MSYS2) | NVIDIA driver ≥ 525 |
| Linux x64 | ✅ | Go ≥ 1.26 + gcc | NVIDIA driver ≥ 525 |
| Linux arm64 | ✅ | Go ≥ 1.26 + gcc | no ORT GPU build (CPU) |
| macOS (Intel/Apple Silicon) | ✅ | Go ≥ 1.26 + clang | none (no CUDA; CPU) |
| Docker (any host) | ✅ | not needed | nvidia-container-toolkit |

No Python at runtime (the tokenizer and the whole input assembly are Go).

## Quick start per platform

**Windows**
```cmd
install.cmd                 :: GPU install (falls back to CPU without an NVIDIA GPU)
install.cmd -Cpu            :: CPU-only, no CUDA downloads
install.cmd -Prebuilt       :: no Go toolchain needed
install.cmd -Service        :: + autostart at boot (elevated prompt)
install.cmd -Plugin         :: + ZCode plugin and skill
```
Manual path: `tools\fetch-model.ps1` → `scripts\windows\fetch-ort.ps1` →
`scripts\windows\fetch-cuda.ps1` (GPU) → `build.cmd` → `start-laya.cmd`.
Autostart is a SYSTEM boot task (`install-autostart.cmd`), plus an optional
port watchdog (`scripts\windows\watchdog\setup-watchdog.ps1`).

**Linux**
```bash
./scripts/install.sh [--cpu|--prebuilt|--service|--plugin]
```
Manual path: `fetch-model.sh` → `fetch-ort.sh [--gpu]` → `fetch-cuda.sh` →
`build.sh` → `laya.sh start|status|logs|stop`. Autostart is a systemd **user**
unit (`systemctl --user status laya-engine`, add `loginctl enable-linger $USER`
to survive logout).

**macOS**
```bash
xcode-select --install
./scripts/install.sh --cpu [--service]     # launchd agent for autostart
```

**Docker**
```bash
./scripts/fetch-model.sh
docker compose up --build laya-cpu
COMPOSE_PROFILES=gpu docker compose up --build laya-gpu
```

## Verify

```bash
curl 127.0.0.1:8095/health    # {"ok":true,...}
curl 127.0.0.1:8095/ready     # {"ready":true}
curl 127.0.0.1:8095/info      # device, checkpoint, limits
python examples/python_client.py    # or node / bash equivalents
```

Expected on checkpoint `multilingual-fp16-0b095e005a4c`: `route=files_write`
(≈0.99), `risk≈0.95`, `data_loss≈0.91`.

## Manual download (no scripts)

Model:
`https://huggingface.co/mizchi/laya-multilingual-onnx/resolve/main/model.onnx`
→ `models/multilingual-fp16.onnx` (SHA-256 must be
`0b095e005a4c295cae74d47b7eb6931c369d48f5b720b45278d774165798310c`).
Tokenizer:
`https://huggingface.co/techtheist/laya-onnx/resolve/main/multilingual/tokenizer.json`
→ `models/tokenizer.json`.
ONNX Runtime 1.25.0: `onnxruntime.dll` / `libonnxruntime.so*` → `bin/`.
CUDA 12 + cuDNN 9 (GPU): `pip download nvidia-cublas-cu12 nvidia-cudnn-cu12
nvidia-cufft-cu12 nvidia-cuda-runtime-cu12 --no-deps -d wheels`, unpack the
shared libraries into `third_party/cuda12/`.

## Uninstall

Stop the service (`./scripts/laya.sh stop` / `stop-laya.cmd`), remove the
autostart unit (`--service` / `uninstall-autostart.cmd` / `launchctl unload`),
delete the repository directory. Everything the engine needs lives inside it.
