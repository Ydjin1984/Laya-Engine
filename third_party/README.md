# third_party/ — библиотеки исполнения

В git ничего из этого не хранится: скачивается скриптами (публичные источники,
лицензии — см. [../NOTICE](../NOTICE)). Раскладка после установки:

| Каталог | Что | Кто скачивает |
|---|---|---|
| `cuda12/` | CUDA 12 + cuDNN 9 (`cudart64_12.dll`, `cublas64_12.dll`, `cublasLt64_12.dll`, `cudnn64_9.dll`, `cufft64_11.dll` и под-DLL cuDNN) | `scripts/fetch-cuda.sh` / `scripts/windows/fetch-cuda.ps1` (колёса PyPI `nvidia-*-cu12`) |
| `onnxruntime-win-x64-gpu-1.25.0/` | ONNX Runtime 1.25.0 для Windows (GPU-сборка) | `scripts/windows/fetch-ort.ps1` |
| `onnxruntime-linux-x64[-gpu]-1.25.0/` | ONNX Runtime 1.25.0 для Linux | `scripts/fetch-ort.sh` |
| `onnxruntime-osx-arm64-1.25.0/` | ONNX Runtime 1.25.0 для macOS | `scripts/fetch-ort.sh` |

## Зачем именно CUDA 12, а не системный Toolkit

CUDA-провайдер ONNX Runtime 1.25 собран под CUDA 12 и ищет конкретные имена:
`cudart64_12.dll`, `cublas64_12.dll`, `cudnn64_9.dll`, `cufft64_11.dll`. Системный
CUDA Toolkit 13.x таких имён не содержит, поэтому библиотеки нужных версий
раскладываются рядом с движком и подмешиваются в путь поиска самим движком
(`engine/cudaenv.go`) или скриптом запуска (`scripts/laya.sh`).

## Если CUDA уже установлена через pip

Движок сам находит пакеты `nvidia-*` в типовых местах Python (Windows:
`AppData\Local\Python\*\Lib\site-packages\nvidia`, Linux/macOS: site-packages
виртуальных окружений и пользовательских установок) — в этом случае
`fetch-cuda.*` не обязателен. Явный список каталогов задаётся переменной
`LAYA_CUDA_DLL_DIRS` (разделитель — системный: `;` в Windows, `:` в Unix).

## Никаких предзагрузок в рантайме

Сервис не скачивает ничего при старте и не ищет библиотеки в сети: отсутствие
файла — это внятная ошибка запуска, а не тихая деградация.
