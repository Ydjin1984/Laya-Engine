package engine

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// refAnswer — ответ эталонной Python-реализации по одному вопросу.
type refAnswer struct {
	Confidence       float64   `json:"confidence"`
	AnswerConfidence float64   `json:"answer_confidence"`
	ActProbability   float64   `json:"act_probability"`
	Probs            []float64 `json:"probs"`
	Choice           string    `json:"choice"`
	Score            float64   `json:"score"`
	Noul             float64   `json:"noul"`
}

type refResult struct {
	ID      string               `json:"id"`
	FactTop string               `json:"fact_top"`
	Answers map[string]refAnswer `json:"answers"`
}

type refFile struct {
	Model         string  `json:"model"`
	Cases         int     `json:"cases"`
	RouteAccuracy float64 `json:"route_accuracy"`
	MajorityBase  float64 `json:"majority_baseline"`
	LatencyMs     struct {
		P50  float64 `json:"p50"`
		P95  float64 `json:"p95"`
		Mean float64 `json:"mean"`
	} `json:"latency_ms"`
	Results []refResult `json:"results"`
}

type caseFile struct {
	Questions json.RawMessage `json:"questions"`
	Cases     []struct {
		ID        string   `json:"id"`
		State     string   `json:"state"`
		FactPacks []string `json:"fact_packs"`
		FactTop   string   `json:"fact_top"`
	} `json:"cases"`
	MaxLen     int `json:"max_len"`
	HeadMaxLen int `json:"head_max_len"`
}

// loadQuestions разбирает объект вопросов, сохраняя порядок ключей: порядок строк
// батча обязан совпадать с эталонным, иначе сравнивать нечего.
func loadQuestions(t *testing.T, raw json.RawMessage) ([]string, []Question) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil { // '{'
		t.Fatalf("questions: %v", err)
	}
	var ids []string
	var qs []Question
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			t.Fatalf("questions key: %v", err)
		}
		id, _ := keyTok.(string)
		var def struct {
			Type         string            `json:"type"`
			Instructions string            `json:"instructions"`
			Criteria     json.RawMessage   `json:"criteria"`
			Labels       map[string]string `json:"labels"`
		}
		if err := dec.Decode(&def); err != nil {
			t.Fatalf("questions[%s]: %v", id, err)
		}
		qt, err := ParseQType(def.Type)
		if err != nil {
			t.Fatalf("questions[%s]: %v", id, err)
		}
		ids = append(ids, id)
		qs = append(qs, Question{
			Type: qt, Instructions: def.Instructions, Criteria: def.Criteria, Labels: def.Labels,
		})
	}
	return ids, qs
}

func findEngineFile(t *testing.T, env, rel, abs string) string {
	t.Helper()
	for _, c := range []string{os.Getenv(env), rel, abs} {
		if c == "" {
			continue
		}
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

// refModelOK проверяет, что эталон снят на тех же весах, которые проверяются.
// Файл эталона хранит путь к модели, которой он снят; сверка fp16-движка с
// int8-эталоном давала тысячи «расхождений», неотличимых от бага движка, хотя
// расходились сами модели.
func refModelOK(t *testing.T, refPath, modelPath string) bool {
	t.Helper()
	raw, err := os.ReadFile(refPath)
	if err != nil {
		return false
	}
	var meta struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil || meta.Model == "" {
		return false
	}
	// Пути в эталонах записаны по-разному (Windows-бэкслеши, относительные):
	// сравниваются только имена файлов.
	base := func(p string) string {
		return path.Base(strings.ReplaceAll(p, `\`, "/"))
	}
	return base(meta.Model) == base(modelPath)
}

// TestDecideMatchesPythonReference — сквозная сверка Go-движка с эталоном:
// сборка последовательности, ONNX-инференс, температурное масштабирование и
// декодирование должны совпасть. Здесь ловится любое расхождение в препроцессинге:
// оно не даёт ошибки, а тихо меняет ответ.
func TestDecideMatchesPythonReference(t *testing.T) {
	if os.Getenv("LAYA_HEAVY") == "" {
		t.Skip("тяжёлый тест: нужен LAYA_HEAVY=1 (загружает ONNX-модель 617 МБ)")
	}
	device := os.Getenv("LAYA_DEVICE")
	if device == "" {
		device = DeviceCUDA
	}
	deviceID := 0
	if v := os.Getenv("LAYA_DEVICE_ID"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			deviceID = n
		}
	}

	casesPath := findEngineFile(t, "LAYA_CASES", filepath.Join("..", "tools", "cases.json"), `D:\Laya_Engine\tools\cases.json`)
	modelPath := findEngineFile(t, "LAYA_MODEL", filepath.Join("..", "models", "multilingual-fp16.onnx"), `D:\Laya_Engine\models\multilingual-fp16.onnx`)
	tokPath := findEngineFile(t, "LAYA_TOKENIZER", filepath.Join("..", "models", "tokenizer.json"), `D:\Laya_Engine\models\tokenizer.json`)
	dllPath := findEngineFile(t, "LAYA_ORT", filepath.Join("..", "bin", "onnxruntime.dll"), `D:\Laya_Engine\bin\onnxruntime.dll`)
	if casesPath == "" || modelPath == "" || tokPath == "" {
		t.Skip("cases.json, модель или токенизатор не найдены — эталон не снят")
	}

	// Эталон обязан совпадать с проверяемыми весами. Файлы различаются:
	//   ref_answers.json       — int8-экспорт (ИСПОРЧЕН квантованием головы; не эталон)
	//   ref_answers_fp16.json  — fp16 на CPU
	//   ref_answers_gpu.json   — fp16 на GPU
	// Сверка fp16-движка с int8-эталоном давала тысячи «расхождений», которые
	// выглядели как баг движка, хотя расходились модели. Поэтому здесь два
	// независимых предохранителя: порядок кандидатов по устройству и проверка
	// поля model в самом файле эталона.
	refPath := os.Getenv("LAYA_REF_ANSWERS")
	if refPath == "" {
		var candidates []string
		if device == DeviceCPU {
			candidates = []string{"ref_answers_fp16.json", "ref_answers_gpu.json"}
		} else {
			candidates = []string{"ref_answers_gpu.json", "ref_answers_fp16.json"}
		}
		for _, name := range candidates {
			p := findEngineFile(t, "", filepath.Join("..", "tools", name), `D:\Laya_Engine\tools\`+name)
			if p == "" {
				continue
			}
			if refModelOK(t, p, modelPath) {
				refPath = p
				break
			}
		}
		if refPath == "" {
			t.Skip("нет эталона, снятого на проверяемых весах: нужен ref_answers_fp16.json " +
				"или ref_answers_gpu.json для multilingual-fp16.onnx (tools/ref_decide.py)")
		}
	} else if !refModelOK(t, refPath, modelPath) {
		t.Skipf("эталон %s снят на другой модели — сверка бессмысленна", filepath.Base(refPath))
	}
	t.Logf("эталон: %s", filepath.Base(refPath))

	rawCases, err := os.ReadFile(casesPath)
	if err != nil {
		t.Fatalf("cases.json: %v", err)
	}
	var cf caseFile
	if err := json.Unmarshal(rawCases, &cf); err != nil {
		t.Fatalf("разбор cases.json: %v", err)
	}
	var ref refFile
	rawRef, err := os.ReadFile(refPath)
	if err != nil {
		t.Fatalf("%s: %v", filepath.Base(refPath), err)
	}
	if err := json.Unmarshal(rawRef, &ref); err != nil {
		t.Fatalf("разбор %s: %v", filepath.Base(refPath), err)
	}

	qids, questions := loadQuestions(t, cf.Questions)
	if len(ref.Results) != len(cf.Cases) {
		t.Fatalf("эталон на %d кейсов, а кейсов %d — переснимите эталон", len(ref.Results), len(cf.Cases))
	}

	m, err := New(Config{
		ModelPath:     modelPath,
		TokenizerPath: tokPath,
		ORTSopath:     dllPath,
		MaxLen:        cf.MaxLen,
		HeadMaxLen:    cf.HeadMaxLen,
		Device:        device,
		DeviceID:      deviceID,
		Temperature:   [3]float64{1, 1, 1}, // rl_agent_config.json этого чекпоинта
	})
	if err != nil {
		t.Fatalf("загрузка модели: %v", err)
	}
	defer m.Close()
	t.Logf("устройство: %s (карта #%d)", m.Device(), deviceID)

	// Допуск: GPU считает в нативном fp16, эталон может быть снят на CPU
	// (fp16-веса приводятся к fp32) или на другой видеокарте — разница только в
	// округлении и порядке суммирования, поэтому допуск 1e-2.
	const tol = 1e-2
	// nearTieGap — разрыв лидеров, при котором выбор варианта неустойчив к
	// аппаратным различиям (см. ветку route ниже).
	const nearTieGap = 1e-2
	mismatches := 0
	choiceMismatch := 0
	nearTies := 0
	compared := 0

	for ci, c := range cf.Cases {
		res, err := m.Decide(c.State, questions)
		if err != nil {
			t.Fatalf("кейс %d: %v", ci, err)
		}
		want := ref.Results[ci]
		if want.ID != c.ID {
			t.Fatalf("порядок кейсов разошёлся: Go %s, эталон %s", c.ID, want.ID)
		}
		for qi, qid := range qids {
			ra, ok := want.Answers[qid]
			if !ok {
				t.Fatalf("в эталоне нет ответа %s", qid)
			}
			got := res.Answers[qi]
			compared++

			if len(got.Probabilities) != len(ra.Probs) {
				mismatches++
				if mismatches <= 5 {
					t.Errorf("кейс %d/%s: вариантов %d, в эталоне %d", ci, qid, len(got.Probabilities), len(ra.Probs))
				}
				continue
			}
			for i := range got.Probabilities {
				if math.Abs(got.Probabilities[i].Value-ra.Probs[i]) > tol {
					mismatches++
					if mismatches <= 5 {
						t.Errorf("кейс %d (%q) / %s: вероятность %d: %.6f, эталон %.6f",
							ci, clipText(c.State), qid, i, got.Probabilities[i].Value, ra.Probs[i])
					}
					break
				}
			}
			if math.Abs(got.AnswerConfidence-ra.AnswerConfidence) > tol {
				mismatches++
				if mismatches <= 5 {
					t.Errorf("кейс %d/%s: answer_confidence %.6f, эталон %.6f", ci, qid, got.AnswerConfidence, ra.AnswerConfidence)
				}
			}
			switch qid {
			case "route":
				if got.Choice != ra.Choice {
					// Почти-равные лидеры: если в эталоне два варианта стоят в
					// пределах nearTieGap, выбор неустойчив к порядку суммирования
					// (другая видеокарта, другой набор ядер) — это не расхождение
					// логики. Измерено: кейс «BTC» (3 символа), эталон 0.3365 против
					// 0.3361, на GTX 1650 лидеры меняются местами. Настоящий разрыв
					// (>= nearTieGap) остаётся ошибкой.
					gap := topGap(ra.Probs)
					if gap < nearTieGap {
						nearTies++
						if nearTies <= 5 {
							t.Logf("кейс %d (%q): почти-равный выбор %q vs %q (разрыв в эталоне %.4f) — зависит от GPU",
								ci, clipText(c.State), got.Choice, ra.Choice, gap)
						}
					} else {
						choiceMismatch++
						mismatches++
						if mismatches <= 8 {
							t.Errorf("кейс %d (%q): выбор %q, эталон %q (разрыв в эталоне %.4f)",
								ci, clipText(c.State), got.Choice, ra.Choice, gap)
						}
					}
				}
			}
			if ra.Noul != 0 && math.Abs(got.Noul-ra.Noul) > tol {
				mismatches++
				if mismatches <= 5 {
					t.Errorf("кейс %d/%s: noul %.6f, эталон %.6f", ci, qid, got.Noul, ra.Noul)
				}
			}
		}
	}

	t.Logf("сверено ответов: %d на %d кейсах", compared, len(cf.Cases))
	if nearTies > 0 {
		t.Logf("почти-равных выборов (разрыв лидеров в эталоне < %.0e): %d — ожидаемо при сверке на другой видеокарте", nearTieGap, nearTies)
	}
	if mismatches > 0 {
		t.Fatalf("расхождений с Python-эталоном: %d (из них по выбору маршрута: %d)", mismatches, choiceMismatch)
	}
	t.Logf("Go-движок совпал с Python-эталоном; эталонная точность маршрута %.3f при мажоритарном baseline %.3f",
		ref.RouteAccuracy, ref.MajorityBase)
}

// topGap — разрыв между двумя лидирующими вероятностями.
func topGap(probs []float64) float64 {
	if len(probs) < 2 {
		return 1
	}
	sorted := append([]float64(nil), probs...)
	sort.Sort(sort.Reverse(sort.Float64Slice(sorted)))
	return sorted[0] - sorted[1]
}

func clipText(s string) string {
	r := []rune(s)
	if len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return s
}
