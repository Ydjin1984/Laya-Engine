// Package server — HTTP-обёртка над движком Laya.
//
// Один локальный сервис обслуживает и Киборг, и Deepseec (§4.3 ТЗ): оба ходят
// сюда по loopback, а не поднимают свои копии весов. Слушает только 127.0.0.1 —
// наружу сервис не выставляется без отдельного решения по безопасности (§16.1).
//
// Контракт намеренно повторяет форму ответа Laya (`answers` по id вопроса), чтобы
// TS-клиент Deepseec и Go-клиент Киборга читали одно и то же.
package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"layaengine/engine"
)

// APIVersion — версия схемы ответа. Клиенты обязаны её проверять: несовпадение
// означает, что поля могли переименоваться, и молча читать такое нельзя (§8.3).
const APIVersion = 1

// Server держит движок и счётчики.
type Server struct {
	model   *engine.Model
	started time.Time

	// ready выставляется в 1 только после успешной загрузки весов: разница между
	// «процесс жив» и «модель готова» — это разница между таймаутом и ответом.
	ready int32

	// checkpoint — какие именно веса загружены. Клиент обязан привязывать свои
	// пороги и логи к этому идентификатору, а не к слову «laya».
	checkpoint engine.Checkpoint

	info ModelInfo
}

// ModelInfo — что именно загружено.
type ModelInfo struct {
	ModelPath  string `json:"model_path"`
	Tokenizer  string `json:"tokenizer_path"`
	MaxLen     int    `json:"max_len"`
	HeadMaxLen int    `json:"head_max_len"`
	// Device — где считает модель на самом деле (cuda|cpu), а не то, что просили.
	Device     string            `json:"device"`
	Revision   string            `json:"revision,omitempty"`
	Checkpoint engine.Checkpoint `json:"checkpoint"`
}

// New создаёт сервер вокруг готовой модели.
func New(m *engine.Model, info ModelInfo) *Server {
	s := &Server{model: m, started: time.Now(), info: info, checkpoint: info.Checkpoint}
	atomic.StoreInt32(&s.ready, 1)
	return s
}

// Routes регистрирует обработчики.
func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/ready", s.handleReady)
	mux.HandleFunc("/info", s.handleInfo)
	mux.HandleFunc("/decide", s.handleDecide)
	mux.HandleFunc("/count", s.handleCount)
	mux.HandleFunc("/benchmark", s.handleBenchmark)
	// Jev-совместимый фасад: тот же движок, но по контракту, который ожидают
	// клиенты laya-server (GET /v1/models, POST /v1/systemone). Нужен потому, что
	// /decide — родной и более богатый контракт этого сервиса, а внешние
	// инструменты (скрипты discovery, чужие обёртки) написаны под Jev-форму.
	mux.HandleFunc("/v1/models", s.handleV1Models)
	mux.HandleFunc("/v1/systemone", s.handleV1Systemone)
}

// DecideRequest — тело /decide.
type DecideRequest struct {
	State     string            `json:"state"`
	Questions json.RawMessage   `json:"questions"`
	MaxLen    int               `json:"max_len,omitempty"`
	Options   map[string]string `json:"options,omitempty"`
}

// Budget — бюджетные факты ответа: сколько текста модель реально увидела.
// Клиент, который игнорирует это поле, может принять уверенный ответ по обрезку
// за уверенный ответ по факту — именно этой ошибке и посвящён §5 ТЗ DaVinci.
type Budget struct {
	StateTokens     int  `json:"state_tokens"`
	StateDroppedMax int  `json:"state_dropped_max"`
	StateTruncated  bool `json:"state_truncated"`
	MaxLen          int  `json:"max_len"`
}

// DecideResponse — ответ /decide.
type DecideResponse struct {
	Version    int                      `json:"version"`
	Model      string                   `json:"model"`
	Checkpoint string                   `json:"checkpoint,omitempty"`
	Answers    map[string]engine.Answer `json:"answers"`
	Order      []string                 `json:"order"`
	Usage      Usage                    `json:"usage"`
	Budget     *Budget                  `json:"budget,omitempty"`
	ElapsedMs  float64                  `json:"elapsed_ms"`
}

// Usage — расход, как в Laya (output_tokens у модели решений всегда 0).
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type errorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "uptime_s": int(time.Since(s.started).Seconds())})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if atomic.LoadInt32(&s.ready) != 1 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ready": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ready": true})
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	st := s.model.Stats()
	writeJSON(w, http.StatusOK, map[string]any{
		"version":         APIVersion,
		"ready":           atomic.LoadInt32(&s.ready) == 1,
		"device":          s.model.Device(),
		"runtime":         "onnxruntime (go, cgo)",
		"model":           s.info,
		"checkpoint":      s.checkpoint,
		"stats":           st,
		"uptime_s":        int(time.Since(s.started).Seconds()),
		"tokenizer_vocab": s.model.Tokenizer().VocabSize(),
	})
}

func (s *Server) handleDecide(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method", "нужен POST")
		return
	}
	// Невалидный JSON — это ошибка клиента, но тексты ошибок не должны содержать
	// сырой ввод: он уходит в логи сервиса.
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	var req DecideRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "тело должно быть JSON")
		return
	}
	if req.State == "" {
		writeError(w, http.StatusBadRequest, "invalid_json", "нужно поле state")
		return
	}
	ids, questions, err := ParseQuestions(req.Questions)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_questions", err.Error())
		return
	}
	if len(questions) == 0 {
		writeJSON(w, http.StatusOK, DecideResponse{
			Version: APIVersion, Model: "laya-go", Checkpoint: s.checkpoint.ID(),
			Answers: map[string]engine.Answer{}, Order: []string{},
		})
		return
	}

	started := time.Now()
	res, err := s.model.DecideWith(req.State, questions, clampRequestMaxLen(req.MaxLen))
	if err != nil {
		log.Printf("[LAYA] decide: %v", err)
		writeError(w, http.StatusInternalServerError, "inference_failed", "инференс не выполнен")
		return
	}

	answers := make(map[string]engine.Answer, len(ids))
	for i, id := range ids {
		a := res.Answers[i]
		a.ID = id
		answers[id] = a
	}
	writeJSON(w, http.StatusOK, DecideResponse{
		Version:    APIVersion,
		Model:      "laya-go",
		Checkpoint: s.checkpoint.ID(),
		Answers:    answers,
		Order:      ids,
		Usage:      Usage{InputTokens: res.InputTokens, OutputTokens: 0},
		Budget: &Budget{
			StateTokens:     res.StateTokens,
			StateDroppedMax: res.StateDroppedMax,
			StateTruncated:  res.Truncated,
			MaxLen:          s.maxLenFor(req.MaxLen),
		},
		ElapsedMs: float64(time.Since(started).Microseconds()) / 1000.0,
	})
}

// clampRequestMaxLen ограничивает per-request переопределение бюджета: слишком
// маленький max_len физически не вмещает вопрос, слишком большой — это уже не
// тот бюджет, под который собирались веса и тесты.
func clampRequestMaxLen(v int) int {
	if v <= 0 {
		return 0
	}
	if v < 64 {
		return 64
	}
	if v > 8192 {
		return 8192
	}
	return v
}

func (s *Server) maxLenFor(requestMaxLen int) int {
	if v := clampRequestMaxLen(requestMaxLen); v > 0 {
		return v
	}
	return s.info.MaxLen
}

// CountRequest — тело /count: те же state и questions, что у /decide.
type CountRequest struct {
	State     string          `json:"state"`
	Questions json.RawMessage `json:"questions"`
	MaxLen    int             `json:"max_len,omitempty"`
}

// CountResponse — бюджеты без инференса.
type CountResponse struct {
	Version    int                    `json:"version"`
	Checkpoint string                 `json:"checkpoint,omitempty"`
	State      int                    `json:"state_tokens"`
	MaxLen     int                    `json:"max_len"`
	HeadMaxLen int                    `json:"head_max_len"`
	Fits       bool                   `json:"fits"`
	Questions  []engine.CountQuestion `json:"questions"`
	ElapsedMs  float64                `json:"elapsed_ms"`
}

// handleCount считает бюджет входа без вызова модели. Клиент-гейт использует его,
// чтобы УКОРОТИТЬ state до инференса, а не узнавать об обрезке по факту.
func (s *Server) handleCount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method", "нужен POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	var req CountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "тело должно быть JSON")
		return
	}
	if req.State == "" {
		writeError(w, http.StatusBadRequest, "invalid_json", "нужно поле state")
		return
	}
	_, questions, err := ParseQuestions(req.Questions)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_questions", err.Error())
		return
	}
	started := time.Now()
	res, err := s.model.CountWith(req.State, questions, clampRequestMaxLen(req.MaxLen))
	if err != nil {
		log.Printf("[LAYA] count: %v", err)
		writeError(w, http.StatusInternalServerError, "count_failed", "замер бюджета не выполнен")
		return
	}
	writeJSON(w, http.StatusOK, CountResponse{
		Version:    APIVersion,
		Checkpoint: s.checkpoint.ID(),
		State:      res.StateTokens,
		MaxLen:     res.MaxLen,
		HeadMaxLen: res.HeadMaxLen,
		Fits:       res.Fits,
		Questions:  res.Questions,
		ElapsedMs:  float64(time.Since(started).Microseconds()) / 1000.0,
	})
}

// handleV1Models — GET /v1/models в форме Jev/OpenAI: клиент узнаёт id модели и
// метаданные чекпоинта ДО первых запросов (§5 ТЗ: «обязательно проверить
// фактически запущенный checkpoint»).
func (s *Server) handleV1Models(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method", "нужен GET")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data": []map[string]any{{
			"id":       "laya-go",
			"object":   "model",
			"owned_by": "local",
			"created":  0,
			"metadata": map[string]any{
				"api_version":       APIVersion,
				"checkpoint":        s.checkpoint.ID(),
				"checkpoint_file":   s.checkpoint.File,
				"checkpoint_bytes":  s.checkpoint.Bytes,
				"checkpoint_sha256": s.checkpoint.SHA256,
				"checkpoint_source": s.checkpoint.Source,
				"device":            s.model.Device(),
				"max_len":           s.info.MaxLen,
				"head_max_len":      s.info.HeadMaxLen,
				"question_types":    []string{"choice", "score", "noul"},
				"native_endpoint":   "/decide",
				"compat_endpoint":   "/v1/systemone",
				"count_endpoint":    "/count",
			},
		}},
	})
}

// V1SystemoneRequest — тело /v1/systemone. Форма совпадает с /decide (state +
// questions), поле model принимается и проверяется, но движок один — это
// осознанное упрощение: сервис держит одну модель на всю машину.
type V1SystemoneRequest struct {
	Model     string          `json:"model"`
	State     string          `json:"state"`
	Questions json.RawMessage `json:"questions"`
	MaxLen    int             `json:"max_len,omitempty"`
}

// handleV1Systemone — Jev-совместимая форма ответа на типизированные вопросы.
func (s *Server) handleV1Systemone(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method", "нужен POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	var req V1SystemoneRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "тело должно быть JSON")
		return
	}
	if req.Model != "" && req.Model != "laya-go" && !strings.HasPrefix(req.Model, "laya") {
		writeError(w, http.StatusNotFound, "unknown_model",
			fmt.Sprintf("модель %q не обслуживается; см. GET /v1/models", req.Model))
		return
	}
	if req.State == "" {
		writeError(w, http.StatusBadRequest, "invalid_json", "нужно поле state")
		return
	}
	ids, questions, err := ParseQuestions(req.Questions)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_questions", err.Error())
		return
	}
	started := time.Now()
	res, err := s.model.DecideWith(req.State, questions, clampRequestMaxLen(req.MaxLen))
	if err != nil {
		log.Printf("[LAYA] systemone: %v", err)
		writeError(w, http.StatusInternalServerError, "inference_failed", "инференс не выполнен")
		return
	}
	answers := make(map[string]engine.Answer, len(ids))
	for i, id := range ids {
		a := res.Answers[i]
		a.ID = id
		answers[id] = a
	}
	writeJSON(w, http.StatusOK, DecideResponse{
		Version:    APIVersion,
		Model:      "laya-go",
		Checkpoint: s.checkpoint.ID(),
		Answers:    answers,
		Order:      ids,
		Usage:      Usage{InputTokens: res.InputTokens, OutputTokens: 0},
		Budget: &Budget{
			StateTokens:     res.StateTokens,
			StateDroppedMax: res.StateDroppedMax,
			StateTruncated:  res.Truncated,
			MaxLen:          s.maxLenFor(req.MaxLen),
		},
		ElapsedMs: float64(time.Since(started).Microseconds()) / 1000.0,
	})
}

// BenchRequest — тело /benchmark.
type BenchRequest struct {
	State       string          `json:"state"`
	Questions   json.RawMessage `json:"questions"`
	Iterations  int             `json:"iterations"`
	Concurrency int             `json:"concurrency"`
}

// BenchResponse — результат замера.
type BenchResponse struct {
	Iterations  int     `json:"iterations"`
	Concurrency int     `json:"concurrency"`
	P50Ms       float64 `json:"p50_ms"`
	P95Ms       float64 `json:"p95_ms"`
	P99Ms       float64 `json:"p99_ms"`
	MeanMs      float64 `json:"mean_ms"`
	MaxMs       float64 `json:"max_ms"`
	RPS         float64 `json:"rps"`
	Answers     int     `json:"answers_per_call"`
	CPU         string  `json:"device"`
}

// handleBenchmark прогоняет один и тот же запрос N раз и считает перцентили.
// Это инструмент приёмки §15: без числа «сколько миллисекунд» включать быстрый
// маршрут нельзя.
func (s *Server) handleBenchmark(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method", "нужен POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	var req BenchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "тело должно быть JSON")
		return
	}
	_, questions, err := ParseQuestions(req.Questions)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_questions", err.Error())
		return
	}
	if req.Iterations <= 0 {
		req.Iterations = 20
	}
	if req.Iterations > 2000 {
		req.Iterations = 2000
	}
	if req.Concurrency <= 0 {
		req.Concurrency = 1
	}
	if req.Concurrency > 16 {
		req.Concurrency = 16
	}

	lat := make([]float64, 0, req.Iterations)
	var mu sync.Mutex
	var wg sync.WaitGroup
	started := time.Now()
	work := make(chan struct{}, req.Iterations)
	for i := 0; i < req.Iterations; i++ {
		work <- struct{}{}
	}
	close(work)

	for c := 0; c < req.Concurrency; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range work {
				t0 := time.Now()
				if _, err := s.model.Decide(req.State, questions); err != nil {
					log.Printf("[LAYA] benchmark: %v", err)
					return
				}
				d := float64(time.Since(t0).Microseconds()) / 1000.0
				mu.Lock()
				lat = append(lat, d)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(lat) == 0 {
		writeError(w, http.StatusInternalServerError, "inference_failed", "ни одного успешного прогона")
		return
	}
	sort.Float64s(lat)
	total := time.Since(started).Seconds()
	writeJSON(w, http.StatusOK, BenchResponse{
		Iterations:  len(lat),
		Concurrency: req.Concurrency,
		P50Ms:       percentile(lat, 0.50),
		P95Ms:       percentile(lat, 0.95),
		P99Ms:       percentile(lat, 0.99),
		MeanMs:      mean(lat),
		MaxMs:       lat[len(lat)-1],
		RPS:         float64(len(lat)) / total,
		Answers:     len(questions),
		CPU:         s.model.Device(),
	})
}

// ParseQuestions принимает вопросы как объект {id: {...}} и сохраняет порядок
// ключей: порядок задаёт порядок строк батча, а он влияет на паддинг и на то,
// какой ответ какому вопросу принадлежит.
func ParseQuestions(raw json.RawMessage) ([]string, []engine.Question, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil, nil
	}
	switch trimmed[0] {
	case '{':
		return parseQuestionObject(trimmed)
	case '[':
		return parseQuestionArray(trimmed)
	}
	return nil, nil, fmt.Errorf("questions: ожидался объект {id: {...}} или массив")
}

func parseQuestionObject(raw []byte) ([]string, []engine.Question, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		return nil, nil, fmt.Errorf("questions: %w", err)
	}
	var ids []string
	var qs []engine.Question
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, nil, fmt.Errorf("questions: %w", err)
		}
		id, _ := keyTok.(string)
		q, err := decodeQuestion(dec)
		if err != nil {
			return nil, nil, fmt.Errorf("questions[%s]: %w", id, err)
		}
		ids = append(ids, id)
		qs = append(qs, q)
	}
	return ids, qs, nil
}

func parseQuestionArray(raw []byte) ([]string, []engine.Question, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil { // '['
		return nil, nil, fmt.Errorf("questions: %w", err)
	}
	var ids []string
	var qs []engine.Question
	i := 0
	for dec.More() {
		q, err := decodeQuestion(dec)
		if err != nil {
			return nil, nil, fmt.Errorf("questions[%d]: %w", i, err)
		}
		ids = append(ids, engine.NormalizeQueryID("", i))
		qs = append(qs, q)
		i++
	}
	return ids, qs, nil
}

func decodeQuestion(dec *json.Decoder) (engine.Question, error) {
	var def struct {
		ID           string            `json:"id"`
		Type         string            `json:"type"`
		Instructions string            `json:"instructions"`
		Criteria     json.RawMessage   `json:"criteria"`
		Labels       map[string]string `json:"labels"`
	}
	if err := dec.Decode(&def); err != nil {
		return engine.Question{}, err
	}
	qt, err := engine.ParseQType(def.Type)
	if err != nil {
		return engine.Question{}, err
	}
	if def.Instructions == "" {
		return engine.Question{}, fmt.Errorf("нужно поле instructions")
	}
	return engine.Question{
		Type:         qt,
		Instructions: def.Instructions,
		Criteria:     def.Criteria,
		Labels:       def.Labels,
	}, nil
}

func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(q * float64(len(sorted)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, code int, kind, msg string) {
	writeJSON(w, code, errorResponse{Error: msg, Code: kind})
}
