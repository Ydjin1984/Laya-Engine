package engine

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	ort "github.com/yalue/onnxruntime_go"

	"layaengine/tokenizer"
)

// Устройства исполнения.
const (
	DeviceCUDA = "cuda"
	DeviceCPU  = "cpu"
)

// Имена входов/выходов ONNX-графа — из onnx_config.json и ref_export.py
// (input_names/output_names при экспорте). Порядок обязан совпадать.
var (
	inputNames  = []string{"input_ids", "attention_mask", "marker_pos", "marker_mask", "qtype"}
	outputNames = []string{"logits", "act_logits"}
)

// Границы температуры — clamp_temperature из laya/common.py. Ниже 0.5 калибровка
// «заостряет» логиты вместо смягчения, и вероятность перестаёт быть вероятностью.
const (
	tempMin = 0.5
	tempMax = 5.0
)

// Config — настройки движка.
type Config struct {
	ModelPath     string // путь к .onnx
	TokenizerPath string // путь к tokenizer.json
	ORTSopath     string // путь к onnxruntime.dll (пусто — искать рядом с exe)
	MaxLen        int
	HeadMaxLen    int
	IntraThreads  int // 0 — по умолчанию ORT

	// OptimizedModelPath — файл кэша оптимизированного графа.
	//
	// Зачем: при загрузке ONNX Runtime прогоняет граф через оптимизации (слияние
	// операторов, выбор ядер под CUDA) — на этой модели это заметная часть времени
	// старта. Если путь задан, ORT сохраняет результат в этот файл и при следующем
	// запуске загружает уже оптимизированную модель. Веса те же (модель лежит в
	// models\ и в сеть не ходит), ускоряется только загрузка.
	//
	// Пустая строка — кэш не используется (поведение по умолчанию без флага).
	OptimizedModelPath string

	// Device — где считать: "cuda" (видеокарта) или "cpu".
	// Пусто означает "cuda": видеокарта свободна, а CPU на этой модели даёт
	// ~1.7 с на батч вопросов вместо десятков миллисекунд.
	Device   string
	DeviceID int
	// AllowCPUFallback разрешает тихий откат на CPU, если CUDA не поднялась.
	// По умолчанию ЗАПРЕЩЁН: молчаливый откат уже один раз привёл к тому, что
	// замеры «на GPU» на самом деле шли на процессоре.
	AllowCPUFallback bool

	// Температуры калибровки: по типу вопроса (choice/score/noul) и по бакету
	// «тип:число вариантов» (например "choice:11+").
	Temperature          [3]float64
	TemperatureByOptions map[string]float64
}

// Model — загруженная модель Laya. Готов к параллельным вызовам: сессия ONNX
// потокобезопасна, состояние в структуре не мутируется после загрузки.
type Model struct {
	tok     *tokenizer.Tokenizer
	session *ort.DynamicAdvancedSession
	cfg     Config

	// device — где РЕАЛЬНО считает модель (cuda|cpu). Именно это значение
	// показывается в диагностике, а не то, что запросили.
	device string

	// stats — счётчики для диагностики.
	mu       sync.Mutex
	requests int64
	failures int64
}

// Answer — типизированный ответ по одному вопросу.
type Answer struct {
	ID   string `json:"id"`
	Type string `json:"type"`

	// Choice — выбранный вариант (только для choice).
	Choice string `json:"choice,omitempty"`
	// Probabilities — распределение по вариантам в порядке их объявления
	// (для score метка — номер уровня).
	Probabilities []Prob `json:"probabilities,omitempty"`
	// Score — ожидаемый уровень (только для score). Без omitempty: 0 — это
	// валидный уровень, и пропавшее поле клиент прочитает как «ответа нет».
	Score float64 `json:"score"`
	// Noul — P(истина) (только для noul). Без omitempty по той же причине:
	// ноль — это осмысленный ответ «нет», а не отсутствие данных.
	Noul float64 `json:"noul"`

	// Confidence — нормированная энтропия: 1 - H/log(k). НЕ калибрована.
	Confidence float64 `json:"confidence"`
	// AnswerConfidence — max(p): именно она калибрована температурой, и именно
	// по ней следует ставить порог допуска маршрута.
	AnswerConfidence float64 `json:"answer_confidence"`
	// ActProbability — P(escalate) из act-головы: сигнал «стоит поднять уровень».
	ActProbability float64 `json:"act_probability"`

	// Collapsed — варианты не различимы после обрезки: ответ недостоверен.
	Collapsed bool `json:"collapsed,omitempty"`

	// StateDropped — сколько токенов состояния не попало в последовательность
	// из-за бюджета max_len (состояние режется справа). Ненулевое значение
	// означает: хвост состояния модель не видела, и «уверенный» ответ может быть
	// уверенным ровно потому, что противоречащая часть текста была отброшена.
	// Клиент обязан учитывать это так же, как Collapsed, а не игнорировать.
	StateDropped int `json:"state_dropped,omitempty"`
	// HeadTruncated — инструкция вопроса не поместилась в head_max_len целиком.
	HeadTruncated bool `json:"head_truncated,omitempty"`
	// Temperature — фактический коэффициент калибровки логитов. Без него
	// answer_confidence нельзя воспроизвести по одному ответу: пороги должны
	// быть привязаны к конкретной температуре и чекпоинту (§9.8 ТЗ DaVinci).
	Temperature float64 `json:"temperature"`
}

// Prob — вероятность одного варианта.
type Prob struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
}

// Result — результат одного вызова.
type Result struct {
	Answers     []Answer `json:"answers"`
	InputTokens int      `json:"input_tokens"`
	ElapsedMs   float64  `json:"elapsed_ms"`
	Batches     int      `json:"batches"`
	Stats       RunStats `json:"stats"`

	// StateTokens — полная длина состояния в токенах (один раз на запрос).
	StateTokens int `json:"state_tokens"`
	// StateDroppedMax — худшая обрезка состояния среди вопросов батча: у разных
	// вопросов разное число вариантов, поэтому и свободного места под состояние
	// разное. Ноль — состояние поместилось целиком во всех вопросах.
	StateDroppedMax int `json:"state_dropped_max"`
	// Truncated — обобщённый флаг: хотя бы один вопрос потерял часть состояния
	// либо варианты «схлопнулись». Именно по нему клиент решает, доверять ли
	// ответам достаточно, чтобы влиять на решения.
	Truncated bool `json:"state_truncated"`
}

// RunStats — счётчики движка.
type RunStats struct {
	Requests int64 `json:"requests"`
	Failures int64 `json:"failures"`
}

// New загружает токенизатор и ONNX-сессию.
func New(cfg Config) (*Model, error) {
	// Файлы весов и словаря лежат локально (models\): молчаливая попытка «взять
	// откуда-нибудь» здесь недопустима — ошибка должна быть внятной и ранней.
	if err := checkReadableFile("модель", cfg.ModelPath); err != nil {
		return nil, err
	}
	if err := checkReadableFile("словарь tokenizer.json", cfg.TokenizerPath); err != nil {
		return nil, err
	}
	if cfg.ORTSopath != "" {
		if err := checkReadableFile("onnxruntime", cfg.ORTSopath); err != nil {
			return nil, err
		}
		ort.SetSharedLibraryPath(cfg.ORTSopath)
	}
	// InitializeEnvironment безопасен при повторном вызове: библиотека сама
	// отслеживает состояние. Держим проверку, чтобы не логировать шум.
	if !ort.IsInitialized() {
		if err := ort.InitializeEnvironment(); err != nil {
			return nil, fmt.Errorf("engine: инициализация onnxruntime: %w", err)
		}
	}

	tok, err := tokenizer.Load(cfg.TokenizerPath)
	if err != nil {
		return nil, err
	}

	// Кэш оптимизированного графа: включается только если путь задан явно.
	cachePath := strings.TrimSpace(cfg.OptimizedModelPath)
	if cachePath != "" {
		if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
			log.Printf("[LAYA] каталог кэша модели недоступен (%v) — кэш отключён", err)
			cachePath = ""
		} else if !optimizedCacheFresh(cachePath, cfg.ModelPath) {
			// Кэш отсутствует или построен по другим весам — начинаем заново.
			_ = os.Remove(cachePath)
			_ = os.Remove(cachePath + ".json")
		}
	}

	sess, device, usedCache, err := openSession(cfg, cachePath)
	if err != nil && usedCache {
		// Кэш мог быть записан другой сборкой ORT или другим провайдером. Это не
		// повод не запускаться: убираем кэш и грузим модель штатным путём.
		log.Printf("[LAYA] кэш оптимизированной модели не подошёл (%v) — удаляю %s, загружаю модель заново", err, cachePath)
		_ = os.Remove(cachePath)
		_ = os.Remove(cachePath + ".json")
		sess, device, _, err = openSession(cfg, "")
	}
	if err != nil {
		return nil, err
	}
	writeCacheMark(cachePath, cfg.ModelPath)

	m := &Model{tok: tok, session: sess, cfg: cfg, device: device}
	for i, t := range cfg.Temperature {
		m.cfg.Temperature[i] = clampTemperature(t)
	}
	return m, nil
}

// openSession собирает опции сессии и создаёт ONNX-сессию. Возвращает фактическое
// устройство (при CPU-fallback оно отличается от запрошенного) и признак того,
// что был задействован кэш оптимизированной модели.
func openSession(cfg Config, cachePath string) (*ort.DynamicAdvancedSession, string, bool, error) {
	device := strings.ToLower(strings.TrimSpace(cfg.Device))
	if device == "" {
		device = DeviceCUDA
	}
	switch device {
	case DeviceCUDA, DeviceCPU:
	default:
		return nil, "", false, fmt.Errorf("engine: неизвестное устройство %q (ожидалось cuda|cpu)", cfg.Device)
	}

	so, err := ort.NewSessionOptions()
	if err != nil {
		return nil, "", false, fmt.Errorf("engine: session options: %w", err)
	}
	defer so.Destroy()
	if cfg.IntraThreads > 0 {
		if err := so.SetIntraOpNumThreads(cfg.IntraThreads); err != nil {
			return nil, "", false, fmt.Errorf("engine: intra-op threads: %w", err)
		}
	}
	// Граф не меняется между вызовами — включаем оптимизации один раз.
	if err := so.SetGraphOptimizationLevel(ort.GraphOptimizationLevelEnableAll); err != nil {
		return nil, "", false, fmt.Errorf("engine: graph optimization: %w", err)
	}

	// Профилирование ORT включается только явно (LAYA_PROFILE=префикс): в обычной
	// работе оно не нужно и добавляет накладные на каждый прогон.
	if p := strings.TrimSpace(os.Getenv("LAYA_PROFILE")); p != "" {
		if err := so.EnableProfiling(p); err != nil {
			log.Printf("[LAYA] профилирование недоступно (%v) — продолжаю без него", err)
		} else {
			log.Printf("[LAYA] профилирование ORT включено: %s_<ts>.json", p)
		}
	}

	usedCache := false
	if cachePath != "" {
		if err := so.SetOptimizedModelFilePath(cachePath); err != nil {
			log.Printf("[LAYA] кэш оптимизированной модели недоступен (%v) — загружаю без кэша", err)
		} else {
			usedCache = true
		}
	}

	if device == DeviceCUDA {
		// Каталоги CUDA 12/cuDNN 9 нужны в PATH до загрузки провайдера.
		ensureCUDALibs()
		if err := attachCUDA(so, cfg.DeviceID); err != nil {
			if !cfg.AllowCPUFallback {
				return nil, "", usedCache, fmt.Errorf("engine: CUDA недоступна (%w); включите AllowCPUFallback, чтобы считать на CPU", err)
			}
			log.Printf("[LAYA] ВНИМАНИЕ: CUDA не поднялась (%v) — считаю на CPU, это медленный режим", err)
			device = DeviceCPU
		}
	}
	// CPU: ничего не добавляем — ORT возьмёт CPUExecutionProvider.

	sess, err := ort.NewDynamicAdvancedSession(cfg.ModelPath, inputNames, outputNames, so)
	if err != nil {
		return nil, "", usedCache, fmt.Errorf("engine: загрузка модели %s: %w", cfg.ModelPath, err)
	}
	return sess, device, usedCache, nil
}

// checkReadableFile проверяет, что локальный файл на месте и не пуст. Веса и
// словарь не скачиваются из сети: если файла нет, запуск должен упасть сразу и
// с внятным текстом, а не после долгого ожидания.
func checkReadableFile(what, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("engine: не задан путь к %s", what)
	}
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("engine: %s не найдена локально (%s): %w", what, path, err)
	}
	if st.IsDir() || st.Size() == 0 {
		return fmt.Errorf("engine: %s пуста или это каталог: %s", what, path)
	}
	return nil
}

// optimizedCacheMark связывает кэш оптимизированного графа с конкретными весами:
// если файл модели заменили, кэш обязан пересобраться, иначе ORT может подсунуть
// граф от старой модели.
type optimizedCacheMark struct {
	Model string `json:"model"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime_unix"`
}

// optimizedCacheFresh отвечает, годится ли существующий кэш для этих весов.
func optimizedCacheFresh(cachePath, modelPath string) bool {
	st, err := os.Stat(cachePath)
	if err != nil || st.Size() == 0 {
		return false
	}
	raw, err := os.ReadFile(cachePath + ".json")
	if err != nil {
		return false
	}
	var mark optimizedCacheMark
	if err := json.Unmarshal(raw, &mark); err != nil {
		return false
	}
	ms, err := os.Stat(modelPath)
	if err != nil {
		return false
	}
	return mark.Size == ms.Size() && mark.MTime == ms.ModTime().Unix()
}

// writeCacheMark записывает метку после успешного создания сессии. Если ORT не
// создал файл кэша (например, путь недоступен на запись), метка не пишется.
func writeCacheMark(cachePath, modelPath string) {
	if cachePath == "" {
		return
	}
	if st, err := os.Stat(cachePath); err != nil || st.Size() == 0 {
		return
	}
	ms, err := os.Stat(modelPath)
	if err != nil {
		return
	}
	raw, err := json.Marshal(optimizedCacheMark{Model: modelPath, Size: ms.Size(), MTime: ms.ModTime().Unix()})
	if err != nil {
		return
	}
	if err := os.WriteFile(cachePath+".json", raw, 0o644); err != nil {
		log.Printf("[LAYA] не удалось записать метку кэша (%v)", err)
	}
}

// attachCUDA подключает CUDAExecutionProvider к опциям сессии.
func attachCUDA(so *ort.SessionOptions, deviceID int) error {
	opts, err := ort.NewCUDAProviderOptions()
	if err != nil {
		return fmt.Errorf("CUDA provider options: %w", err)
	}
	defer opts.Destroy()
	epOpts := map[string]string{"device_id": strconv.Itoa(deviceID)}
	// Дополнительные опции CUDA EP задаются JSON-строкой в LAYA_EP_OPTIONS
	// (например, {"enable_cuda_graph":"1"}). Нужны для замеров: включать
	// экспериментальные режимы в коде по умолчанию рано, пока они не измерены.
	if raw := strings.TrimSpace(os.Getenv("LAYA_EP_OPTIONS")); raw != "" {
		var extra map[string]string
		if err := json.Unmarshal([]byte(raw), &extra); err != nil {
			log.Printf("[LAYA] LAYA_EP_OPTIONS не разобран (%v) — игнорирую", err)
		} else {
			for k, v := range extra {
				epOpts[k] = v
			}
			log.Printf("[LAYA] опции CUDA EP: %v", extra)
		}
	}
	if err := opts.Update(epOpts); err != nil {
		return fmt.Errorf("CUDA options: %w", err)
	}
	if err := so.AppendExecutionProviderCUDA(opts); err != nil {
		return fmt.Errorf("AppendExecutionProviderCUDA: %w", err)
	}
	return nil
}

// Device возвращает устройство, на котором реально считает модель.
func (m *Model) Device() string { return m.device }

// Close освобождает сессию.
func (m *Model) Close() error {
	if m.session != nil {
		return m.session.Destroy()
	}
	return nil
}

// Tokenizer даёт доступ к токенизатору (нужен тестам и бенчмарку).
func (m *Model) Tokenizer() *tokenizer.Tokenizer { return m.tok }

// Stats возвращает счётчики.
func (m *Model) Stats() RunStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	return RunStats{Requests: m.requests, Failures: m.failures}
}

// Decide отвечает на батч вопросов по одному состоянию. Все вопросы уходят в
// модель одним прогоном (как predict_batch в onnx_agent.py): состояние
// токенизируется один раз, а строки батча — это вопросы.
func (m *Model) Decide(state string, questions []Question) (*Result, error) {
	return m.DecideWith(state, questions, 0)
}

// DecideWith — то же, но с переопределением бюджета max_len на один запрос
// (0 — бюджет из конфигурации модели). Нужно клиентам, которые сознательно
// укорачивают вход, чтобы уложиться целиком и не терять хвост состояния.
func (m *Model) DecideWith(state string, questions []Question, maxLenOverride int) (*Result, error) {
	if len(questions) == 0 {
		return &Result{Stats: m.Stats()}, nil
	}
	started := time.Now()

	cfg := SeqConfig{MaxLen: m.cfg.MaxLen, HeadMaxLen: m.cfg.HeadMaxLen}
	if maxLenOverride > 0 {
		cfg.MaxLen = maxLenOverride
	}
	stateIDs := EncodeState(m.tok, state)

	n := len(questions)
	ids := make([][]int32, n)
	markers := make([][]int, n)
	stats := make([]SeqStats, n)
	qtypes := make([]int64, n)

	maxLen, maxMarkers := 1, 1
	for i, q := range questions {
		seq, mk, st, err := BuildSequence(m.tok, stateIDs, q, cfg)
		if err != nil {
			m.noteFailure()
			return nil, fmt.Errorf("engine: вопрос %d: %w", i, err)
		}
		ids[i], markers[i], stats[i] = seq, mk, st
		qtypes[i] = int64(q.Type)
		if len(seq) > maxLen {
			maxLen = len(seq)
		}
		if len(mk) > maxMarkers {
			maxMarkers = len(mk)
		}
	}

	// --- раскладка батча (collate_items) ---
	inputIDs := make([]int64, n*maxLen)
	attMask := make([]int64, n*maxLen)
	markerPos := make([]int64, n*maxMarkers)
	markerMask := make([]bool, n*maxMarkers)
	totalTokens := 0
	for i := range ids {
		for j, id := range ids[i] {
			inputIDs[i*maxLen+j] = int64(id)
			attMask[i*maxLen+j] = 1
		}
		totalTokens += len(ids[i])
		for j, mp := range markers[i] {
			markerPos[i*maxMarkers+j] = int64(mp)
			markerMask[i*maxMarkers+j] = true
		}
	}

	// --- тензоры ---
	shape := ort.NewShape(int64(n), int64(maxLen))
	tIDs, err := ort.NewTensor(shape, inputIDs)
	if err != nil {
		m.noteFailure()
		return nil, fmt.Errorf("engine: тензор input_ids: %w", err)
	}
	defer tIDs.Destroy()
	tMask, err := ort.NewTensor(shape, attMask)
	if err != nil {
		m.noteFailure()
		return nil, fmt.Errorf("engine: тензор attention_mask: %w", err)
	}
	defer tMask.Destroy()

	mShape := ort.NewShape(int64(n), int64(maxMarkers))
	tPos, err := ort.NewTensor(mShape, markerPos)
	if err != nil {
		m.noteFailure()
		return nil, fmt.Errorf("engine: тензор marker_pos: %w", err)
	}
	defer tPos.Destroy()
	tMMask, err := ort.NewTensor(mShape, markerMask)
	if err != nil {
		m.noteFailure()
		return nil, fmt.Errorf("engine: тензор marker_mask: %w", err)
	}
	defer tMMask.Destroy()
	tQType, err := ort.NewTensor(ort.NewShape(int64(n)), qtypes)
	if err != nil {
		m.noteFailure()
		return nil, fmt.Errorf("engine: тензор qtype: %w", err)
	}
	defer tQType.Destroy()

	logits, err := ort.NewEmptyTensor[float32](mShape)
	if err != nil {
		m.noteFailure()
		return nil, fmt.Errorf("engine: выход logits: %w", err)
	}
	defer logits.Destroy()
	acts, err := ort.NewEmptyTensor[float32](ort.NewShape(int64(n), int64(2)))
	if err != nil {
		m.noteFailure()
		return nil, fmt.Errorf("engine: выход act_logits: %w", err)
	}
	defer acts.Destroy()

	if err := m.session.Run(
		[]ort.Value{tIDs, tMask, tPos, tMMask, tQType},
		[]ort.Value{logits, acts},
	); err != nil {
		m.noteFailure()
		return nil, fmt.Errorf("engine: инференс: %w", err)
	}

	logitData := logits.GetData()
	actData := acts.GetData()

	// act_logits — сырые логиты двух действий, а не вероятность. Эталон
	// (onnx_agent.py) считает softmax по ним вручную; без этого в поле
	// act_probability уезжали значения вида 1504.
	actProbs := softmax2(actData)

	out := &Result{
		Answers:     make([]Answer, n),
		InputTokens: totalTokens,
		ElapsedMs:   float64(time.Since(started).Microseconds()) / 1000.0,
		Batches:     1,
		StateTokens: len(stateIDs),
	}
	for i, q := range questions {
		row := logitData[i*maxMarkers : i*maxMarkers+len(markers[i])]
		ans, err := m.decode(q, row, actProbs[i], markers[i], stats[i])
		if err != nil {
			m.noteFailure()
			return nil, fmt.Errorf("engine: вопрос %d: %w", i, err)
		}
		out.Answers[i] = ans
		if d := stats[i].StateDropped(); d > out.StateDroppedMax {
			out.StateDroppedMax = d
		}
		if stats[i].Collapsed() || stats[i].StateDropped() > 0 || stats[i].HeadTruncated {
			out.Truncated = true
		}
	}
	out.Stats = m.noteSuccess()
	return out, nil
}

// CountResult — бюджеты без инференса: сколько токенов займут состояние и вопрос,
// помещается ли состояние целиком и не «схлопнулись» ли варианты.
//
// Зачем отдельный вызов: точную обрезку делает BuildSequence при сборке, но
// узнать о ней заранее (и успеть укоротить state до вызова модели) можно только
// здесь. Токенизация стоит миллисекунды и не занимает видеокарту, поэтому
// клиент-гейт может проверять бюджет на каждом вызове, а не догадываться.
type CountResult struct {
	StateTokens int             `json:"state_tokens"`
	MaxLen      int             `json:"max_len"`
	HeadMaxLen  int             `json:"head_max_len"`
	Questions   []CountQuestion `json:"questions"`
	// Fits — true, если ни один вопрос не потеряет часть состояния и варианты не
	// схлопнулись: ответам можно доверять по бюджетной части.
	Fits bool `json:"fits"`
}

// CountQuestion — бюджет одного вопроса.
type CountQuestion struct {
	Options         int  `json:"options"`
	OptionsDistinct int  `json:"options_distinct"`
	Collapsed       bool `json:"collapsed"`
	HeadTokens      int  `json:"head_tokens"`
	HeadTruncated   bool `json:"head_truncated"`
	OptionTokens    int  `json:"option_tokens"`
	StateKept       int  `json:"state_kept"`
	StateDropped    int  `json:"state_dropped"`
	SeqLen          int  `json:"seq_len"`
	Fits            bool `json:"fits"`
}

// Count считает бюджеты состояния и вопросов без вызова модели.
func (m *Model) Count(state string, questions []Question) (*CountResult, error) {
	return m.CountWith(state, questions, 0)
}

// CountWith — то же, но с переопределением бюджета max_len на один замер
// (0 — бюджет из конфигурации модели).
func (m *Model) CountWith(state string, questions []Question, maxLenOverride int) (*CountResult, error) {
	cfg := SeqConfig{MaxLen: m.cfg.MaxLen, HeadMaxLen: m.cfg.HeadMaxLen}
	if maxLenOverride > 0 {
		cfg.MaxLen = maxLenOverride
	}
	cfg = cfg.Normalize()
	stateIDs := EncodeState(m.tok, state)
	out := &CountResult{
		StateTokens: len(stateIDs),
		MaxLen:      cfg.MaxLen,
		HeadMaxLen:  cfg.HeadMaxLen,
		Questions:   make([]CountQuestion, 0, len(questions)),
		Fits:        true,
	}
	for i, q := range questions {
		ids, _, st, err := BuildSequence(m.tok, stateIDs, q, cfg)
		if err != nil {
			return nil, fmt.Errorf("engine: вопрос %d: %w", i, err)
		}
		cq := CountQuestion{
			Options:         st.Options,
			OptionsDistinct: st.OptionsDistinct,
			Collapsed:       st.Collapsed(),
			HeadTokens:      st.HeadTokens,
			HeadTruncated:   st.HeadTruncated,
			OptionTokens:    st.OptionTokens,
			StateKept:       st.StateKept,
			StateDropped:    st.StateDropped(),
			SeqLen:          len(ids),
		}
		cq.Fits = !cq.Collapsed && !cq.HeadTruncated && cq.StateDropped == 0
		if !cq.Fits {
			out.Fits = false
		}
		out.Questions = append(out.Questions, cq)
	}
	return out, nil
}

// softmax2 превращает act_logits [N,2] в вероятности P(действие) для каждой строки.
func softmax2(act []float32) [][2]float64 {
	n := len(act) / 2
	out := make([][2]float64, n)
	for i := 0; i < n; i++ {
		a, b := float64(act[i*2]), float64(act[i*2+1])
		m := math.Max(a, b)
		ea, eb := math.Exp(a-m), math.Exp(b-m)
		sum := ea + eb
		if sum == 0 {
			out[i] = [2]float64{0, 0}
			continue
		}
		out[i] = [2]float64{ea / sum, eb / sum}
	}
	return out
}

// decode повторяет ONNXAgent._decode_answers.
func (m *Model) decode(q Question, logits []float32, act [2]float64, markers []int, st SeqStats) (Answer, error) {
	k := len(markers)
	if k == 0 {
		return Answer{}, fmt.Errorf("нет маркеров")
	}
	qt := int(q.Type)
	tScale := m.temperatureFor(qt, k)

	// Температурное масштабирование и softmax — в float64, как numpy.
	z := make([]float64, k)
	zmax := math.Inf(-1)
	for i := 0; i < k; i++ {
		z[i] = float64(logits[i]) / tScale
		if z[i] > zmax {
			zmax = z[i]
		}
	}
	var sum float64
	for i := range z {
		z[i] = math.Exp(z[i] - zmax)
		sum += z[i]
	}
	p := make([]float64, k)
	for i := range z {
		p[i] = z[i] / sum
	}

	ans := Answer{
		Type:             q.Type.Name(),
		Confidence:       round4(entropyConfidence(p)),
		AnswerConfidence: round4(maxProb(p)),
		ActProbability:   round4(act[0]),
		Collapsed:        st.Collapsed(),
		StateDropped:     st.StateDropped(),
		HeadTruncated:    st.HeadTruncated,
		Temperature:      round4(tScale),
	}

	switch q.Type {
	case QChoice:
		opts, err := RenderOptions(q)
		if err != nil {
			return Answer{}, err
		}
		keys, err := choiceLabels(q)
		if err != nil {
			return Answer{}, err
		}
		if len(keys) != k {
			return Answer{}, fmt.Errorf("меток %d, а маркеров %d", len(keys), k)
		}
		best := 0
		for i := 1; i < k; i++ {
			if p[i] > p[best] {
				best = i
			}
		}
		ans.Choice = keys[best]
		ans.Probabilities = make([]Prob, k)
		for i := 0; i < k; i++ {
			ans.Probabilities[i] = Prob{Label: keys[i], Value: round4(p[i])}
		}
		_ = opts

	case QScore:
		var expected float64
		ans.Probabilities = make([]Prob, k)
		for i := 0; i < k; i++ {
			expected += float64(i) * p[i]
			ans.Probabilities[i] = Prob{Label: fmt.Sprintf("%d", i), Value: round4(p[i])}
		}
		ans.Score = round4(expected)

	case QNoul:
		if k < 2 {
			return Answer{}, fmt.Errorf("noul требует два варианта, получено %d", k)
		}
		// Распределение отдаётся и здесь: клиент сравнивает ответы разных типов
		// по одному числу, и «нет вероятностей у noul» ломает такое сравнение.
		labels := []string{"false", "true"}
		if q.Labels != nil {
			for i, key := range labels {
				if v := strings.TrimSpace(q.Labels[key]); v != "" {
					labels[i] = v
				}
			}
		}
		ans.Probabilities = make([]Prob, k)
		for i := 0; i < k; i++ {
			label := labels[i]
			if i >= len(labels) {
				label = fmt.Sprintf("%d", i)
			}
			ans.Probabilities[i] = Prob{Label: label, Value: round4(p[i])}
		}
		ans.Noul = round4(p[1])
	}
	return ans, nil
}

// choiceLabels возвращает метки вариантов в порядке объявления — это же порядок,
// в котором модель выдаёт вероятности.
func choiceLabels(q Question) ([]string, error) {
	pairs, err := orderedObject(q.Criteria)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, p.Label)
	}
	return out, nil
}

func (m *Model) temperatureFor(qtype, k int) float64 {
	bucket := tempBucket(qtype, k)
	if m.cfg.TemperatureByOptions != nil {
		if t, ok := m.cfg.TemperatureByOptions[bucket]; ok {
			return clampTemperature(t)
		}
	}
	if qtype >= 0 && qtype < len(m.cfg.Temperature) {
		return clampTemperature(m.cfg.Temperature[qtype])
	}
	return 1.0
}

// tempBucket повторяет temp_bucket из laya/common.py.
func tempBucket(qtype, k int) string {
	var size string
	switch {
	case k <= 2:
		size = "2"
	case k <= 5:
		size = "3-5"
	case k <= 10:
		size = "6-10"
	default:
		size = "11+"
	}
	return QType(qtype).Name() + ":" + size
}

func clampTemperature(t float64) float64 {
	if math.IsNaN(t) || math.IsInf(t, 0) {
		return 1.0
	}
	if t < tempMin {
		return tempMin
	}
	if t > tempMax {
		return tempMax
	}
	return t
}

// entropyConfidence — confidence_from_probs: 1 - H/log(k). Не калибрована.
func entropyConfidence(p []float64) float64 {
	k := len(p)
	if k < 2 {
		return 1.0
	}
	var h float64
	for _, v := range p {
		c := math.Max(v, 1e-12)
		h -= v * math.Log(c)
	}
	c := 1.0 - h/math.Log(float64(k))
	return math.Max(0, math.Min(1, c))
}

// maxProb — answer_confidence: max(p). Именно эту величину калибрует
// температурное масштабирование, поэтому порог допуска ставится по ней.
func maxProb(p []float64) float64 {
	best := 0.0
	for _, v := range p {
		if v > best {
			best = v
		}
	}
	return math.Max(0, math.Min(1, best))
}

func round4(v float64) float64 {
	return math.Round(v*10000) / 10000
}

func (m *Model) noteFailure() {
	m.mu.Lock()
	m.failures++
	m.mu.Unlock()
}

func (m *Model) noteSuccess() RunStats {
	m.mu.Lock()
	m.requests++
	s := RunStats{Requests: m.requests, Failures: m.failures}
	m.mu.Unlock()
	return s
}

// QuestionIDs — список id вопросов (для диагностики и стабильного порядка).
func QuestionIDs(qs []Question) []string {
	out := make([]string, 0, len(qs))
	for range qs {
		out = append(out, "")
	}
	return out
}

// SortByLen — стабильная сортировка по длине (для батчинга похожих запросов).
func SortByLen(items []string) []string {
	out := append([]string{}, items...)
	sort.SliceStable(out, func(i, j int) bool { return len(out[i]) < len(out[j]) })
	return out
}

// TruncateState обрезает состояние по бюджету символов (грубая защита от
// гигантских входов до токенизации; точную обрезку делает BuildSequence).
func TruncateState(state string, maxChars int) string {
	if maxChars <= 0 || len(state) <= maxChars {
		return state
	}
	r := []rune(state)
	if len(r) <= maxChars {
		return state
	}
	return string(r[:maxChars])
}

// NormalizeQueryID — id вопроса не должен быть пустым (используется в ответе).
func NormalizeQueryID(id string, idx int) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Sprintf("q%d", idx)
	}
	return id
}
