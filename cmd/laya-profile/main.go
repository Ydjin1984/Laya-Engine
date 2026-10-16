// laya-profile — снять ORT-профиль на типичном сценарии риск-гейта.
//
// Зачем отдельная утилита: ORT пишет файл профиля только при уничтожении
// сессии (EndProfiling), а убить боевой сервер на Windows «по-хорошему»
// (Ctrl+C из скрипта) нельзя — signal.Notify не получает событие от taskkill.
// Здесь сессия создаётся и закрывается в одном процессе: профиль гарантированно
// сбрасывается на диск через defer m.Close().
//
// Запуск (из корня движка, без сети, только локальные веса):
//
//	bin\go run ./cmd/laya-profile -n 12
//
// Файл профиля: logs\ort-profile_<ts>.json (префикс задаётся флагом -profile).
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"layaengine/engine"
)

func main() {
	model := flag.String("model", "models/multilingual-fp16.onnx", "путь к ONNX-модели")
	tokPath := flag.String("tokenizer", "models/tokenizer.json", "путь к tokenizer.json")
	ortPath := flag.String("ort", "bin/onnxruntime.dll", "путь к onnxruntime.dll")
	profile := flag.String("profile", "logs/ort-profile", "префикс файла профиля")
	n := flag.Int("n", 12, "число повторов сценария")
	flag.Parse()

	// Профилирование включается в openSession по этому env — тем же путём,
	// что и у сервера.
	if err := os.Setenv("LAYA_PROFILE", *profile); err != nil {
		log.Printf("не удалось выставить LAYA_PROFILE: %v", err)
	}
	engine.PrepareCUDALibs()

	m, err := engine.New(engine.Config{
		ModelPath:     *model,
		TokenizerPath: *tokPath,
		ORTSopath:     *ortPath,
		MaxLen:        1024,
		HeadMaxLen:    256,
		Device:        engine.DeviceCUDA,
		DeviceID:      0,
		Temperature:   [3]float64{1, 1, 1},
	})
	if err != nil {
		log.Fatalf("модель не загружена: %v", err)
	}
	// Destroy сессии — единственный момент, когда ORT сбрасывает профиль.
	defer func() {
		if err := m.Close(); err != nil {
			log.Printf("close: %v", err)
		}
	}()

	// Вопросы повторяют риск-гейт плагина (noul + choice + score).
	questions := []engine.Question{
		{Type: engine.QNoul, Instructions: "Уничтожит ли это действие несохранённые данные?"},
		{Type: engine.QChoice, Instructions: "Категория риска?",
			Criteria: []byte(`{"low": "безопасно", "medium": "восстановимо", "high": "потеря данных"}`)},
		{Type: engine.QScore, Instructions: "Сложность?",
			Criteria: []byte(`["просто", "средне", "сложно"]`)},
	}

	// Два профиля длины: короткий state (типичная read-команда) и длинный
	// (команда + git-доказательство — верхняя граница реальных вызовов).
	states := map[string]string{
		"short": "Инструмент: Bash\nКоманда: git status --short\nПуть: (нет)",
		"long": "Инструмент: Bash\nКоманда: " + strings.Repeat("x", 600) +
			"\nПуть: build (внутри проекта)\nПравила: truncate-file(medium)\nGit: " + strings.Repeat("y", 400),
	}

	// Разогрев (CUDA-ядра, буферы) — чтобы в профиль не попал холодный старт.
	if _, err := m.Decide(states["short"], questions); err != nil {
		log.Fatalf("прогрев: %v", err)
	}

	for _, name := range []string{"short", "long"} {
		state := states[name]
		lat := make([]float64, 0, *n)
		for i := 0; i < *n; i++ {
			t0 := time.Now()
			res, err := m.Decide(state, questions)
			if err != nil {
				log.Fatalf("%s: %v", name, err)
			}
			lat = append(lat, float64(time.Since(t0).Microseconds())/1000.0)
			_ = res
		}
		var sum float64
		for _, v := range lat {
			sum += v
		}
		fmt.Printf("%s: n=%d, среднее %.1f мс, первый %.1f мс, последний %.1f мс\n",
			name, *n, sum/float64(len(lat)), lat[0], lat[len(lat)-1])
	}
	fmt.Println("профиль будет записан при закрытии сессии (сейчас)")
}
