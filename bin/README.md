# bin/ — бинарники и библиотеки исполнения

В git не хранится ничего из этого каталога (кроме `.gitkeep` и этого файла):
сначала установка (`.cmd`/`.sh` скрипты сборки и загрузки), потом запуск.

| Файл | Кто создаёт |
|---|---|
| `laya-server.exe` (Windows) / `laya-server` (Linux/macOS) | `build.cmd` / `scripts/build.sh` (или релизный ассет) |
| `libonnxruntime.so*` / `libonnxruntime*.dylib` | `scripts/fetch-ort.sh` |
| `onnxruntime.dll`, `onnxruntime_providers_cuda.dll`, `onnxruntime_providers_shared.dll` | `scripts/windows/fetch-ort.ps1` |

Движок ищет `onnxruntime` так: явный путь (`-ort` / `LAYA_ORT`) → рядом с
исполняемым файлом → стандартный поиск загрузчика. CUDA-библиотеки — см.
[../third_party/README.md](../third_party/README.md).
