# Docker

Два образа: CPU (работает везде) и GPU (NVIDIA, Linux-хост).

| Файл | Что это | Требования |
|---|---|---|
| `Dockerfile` | CPU-образ на `debian:bookworm-slim` | Docker 20+ |
| `Dockerfile.gpu` | GPU-образ на `nvidia/cuda:12.6.3-cudnn-runtime-ubuntu24.04` | Docker + [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html) |
| `docker-compose.yml` | оба сценария: профиль `cpu` (по умолчанию) и профиль `gpu` | Docker Compose v2 |

## Как запустить

```bash
# 1) один раз: веса и токенизатор (те же файлы использует и локальная установка)
./scripts/fetch-model.sh          # Windows: powershell tools\fetch-model.ps1

# 2) CPU
docker compose up --build laya-cpu
curl http://127.0.0.1:8095/info

# 3) GPU (Linux, нужен nvidia-container-toolkit)
COMPOSE_PROFILES=gpu docker compose up --build laya-gpu
```

Порт публикуется только на loopback: `127.0.0.1:8095:8095` — как и у локальной
установки, сервис не предназначен для публикации наружу.

## Что внутри

- **Сборка** (`builder`): `golang:1.26-bookworm`, cgo + gcc, скачивание ONNX
  Runtime 1.25.0 (CPU или GPU-вариант) с официального релиза Microsoft,
  `go build ./cmd/laya-server`.
- **Рантайм**: `debian:bookworm-slim` + `curl` (healthcheck), бинарь,
  `libonnxruntime*.so`, каталог `models/` монтируется томом (веса не в образе —
  так образ остаётся ~100 МБ вместо ~750 МБ).
- GPU-образ ставит CUDA 12 runtime + cuDNN 9 из базового образа NVIDIA,
  поэтому `scripts/fetch-cuda.sh` внутри не нужен.

## Проверка

```bash
curl -fsS http://127.0.0.1:8095/health     # процесс жив
curl -fsS http://127.0.0.1:8095/ready      # модель загружена
curl -fsS http://127.0.0.1:8095/info       # устройство cuda|cpu, чекпоинт
```

> Образы предоставляются как удобный способ запуска на серверах и в CI; на этой
> машине они не собирались (Docker не установлен). Если что-то не собирается —
> issue с логом сборки, поправим быстро.

## Замечания

- Внутри контейнера сервис слушает `0.0.0.0:8095` (иначе порт не выйдет из
  контейнера), но публикация привязана к `127.0.0.1` на хосте. Не меняйте
  публикацию на `0.0.0.0` без reverse-proxy с аутентификацией.
- Веса монтируются read-only: `./models:/app/models:ro`.
- Модель загружается в память процесса (~700 МБ RSS для fp16 + CUDA-арены);
  дайте контейнеру не меньше 2 ГБ RAM (GPU-варианту — VRAM 2 ГБ+).
