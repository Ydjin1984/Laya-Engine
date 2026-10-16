package engine

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Разделяемые библиотеки CUDA, без которых не грузится CUDA-провайдер
// onnxruntime: cudart64_12.dll, cublas64_12.dll, cublasLt64_12.dll,
// cudnn64_9.dll, cufft64_11.dll.
//
// Windows ищет зависимости DLL по PATH в момент загрузки, поэтому каталоги
// нужно добавить ДО ort.InitializeEnvironment — после этого уже поздно.
// Системный CUDA Toolkit (в этой машине v13.3) этих имён не содержит: ORT 1.25
// собран под CUDA 12, и подходящие DLL берутся либо из third_party\cuda12 рядом
// с движком, либо из nvidia-* пакетов Python.

var cudaEnvOnce sync.Once

// cudaLibDirsFromEnv возвращает каталоги, заданные явно. Разделитель — системный
// (';' на Windows, ':' на Linux/macOS): один и тот же LAYA_CUDA_DLL_DIRS должен
// работать на всех платформах.
func cudaLibDirsFromEnv() []string {
	v := strings.TrimSpace(os.Getenv("LAYA_CUDA_DLL_DIRS"))
	if v == "" {
		return nil
	}
	return filepath.SplitList(v)
}

// defaultCUDALibDirs перечисляет типовые места, где лежат DLL CUDA 12 и cuDNN 9.
func defaultCUDALibDirs() []string {
	var out []string
	if exe, err := os.Executable(); err == nil {
		base := filepath.Dir(exe)
		// Рядом с бинарём и в подкаталоге cuda — раскладка, которую делает build.cmd.
		out = append(out, base, filepath.Join(base, "cuda"))
		// serve.exe лежит в bin\, а DLL CUDA 12 — в third_party\cuda12 у корня
		// проекта: путь считается от расположения бинаря, а не от текущего каталога,
		// иначе запуск «откуда угодно» терял библиотеки.
		out = append(out, filepath.Join(base, "..", "third_party", "cuda12"))
	}
	// Рабочий каталог (запуск из корня движка).
	out = append(out, filepath.Join("third_party", "cuda12"))

	// Пакеты Python nvidia-*: cudnn, cublas, cuda_runtime, cufft.
	roots := []string{}
	if v := os.Getenv("LAYA_PY_SITE"); v != "" {
		roots = append(roots, filepath.Join(v, "nvidia"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		// Windows: AppData\Local\Python\*\Lib\site-packages\nvidia (pip user install).
		matches, _ := filepath.Glob(filepath.Join(home, "AppData", "Local", "Python", "*", "Lib", "site-packages", "nvidia"))
		roots = append(roots, matches...)
		// Linux/macOS: локальные site-packages (venv и пользовательский pip).
		globs := []string{
			filepath.Join(home, ".local", "lib", "python3*", "site-packages", "nvidia"),
			filepath.Join(home, ".venv", "lib", "python3*", "site-packages", "nvidia"),
			filepath.Join(home, "venv", "lib", "python3*", "site-packages", "nvidia"),
			"/usr/local/lib/python3*/site-packages/nvidia",
			"/usr/lib/python3/dist-packages/nvidia",
			filepath.Join(home, "Library", "Python", "*", "lib", "python*", "site-packages", "nvidia"),
		}
		for _, g := range globs {
			m, _ := filepath.Glob(g)
			roots = append(roots, m...)
		}
	}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			// Windows-пакеты nvidia-* кладут DLL в bin\, Linux/macOS — в lib\.
			out = append(out, filepath.Join(root, e.Name(), "bin"), filepath.Join(root, e.Name(), "lib"))
		}
	}
	return out
}

// PrepareCUDALibs подключает каталоги CUDA-библиотек к PATH ДО инициализации
// onnxruntime. Вызывается из main до engine.New; повторные вызовы — no-op.
func PrepareCUDALibs() []string {
	return ensureCUDALibs()
}

// ensureCUDALibs добавляет каталоги CUDA-библиотек в PATH процесса. Вызывается
// один раз за процесс; ошибки не фатальны — если библиотек нет, отказ произойдёт
// при создании сессии с внятным сообщением.
//
// На Windows PATH — штатный механизм поиска DLL. На Linux/macOS динамический
// загрузчик PATH не использует: там каталоги задаёт стартовый скрипт через
// LD_LIBRARY_PATH/DYLD_LIBRARY_PATH (scripts/laya.sh, systemd-юнит), а этот код
// остаётся безвредным дополнением для тестов и прямого запуска.
func ensureCUDALibs() []string {
	var added []string
	cudaEnvOnce.Do(func() {
		sep := string(os.PathListSeparator)
		path := os.Getenv("PATH")
		lower := strings.ToLower(path)
		dirs := append(cudaLibDirsFromEnv(), defaultCUDALibDirs()...)
		seen := map[string]bool{}
		for _, d := range dirs {
			d = strings.TrimSpace(d)
			if d == "" || seen[strings.ToLower(d)] {
				continue
			}
			seen[strings.ToLower(d)] = true
			st, err := os.Stat(d)
			if err != nil || !st.IsDir() {
				continue
			}
			if strings.Contains(lower, strings.ToLower(d)) {
				continue
			}
			path = d + sep + path
			lower = strings.ToLower(d) + sep + lower
			added = append(added, d)
		}
		if len(added) > 0 {
			_ = os.Setenv("PATH", path)
			log.Printf("[LAYA] каталоги библиотек CUDA добавлены в PATH: %s", strings.Join(added, ", "))
		}
	})
	return added
}
