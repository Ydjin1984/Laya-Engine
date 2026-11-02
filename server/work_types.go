// Package server — типы событий панели Laya Work.
//
// Событие — это факт, а не объяснение: вход, вопрос, ответ модели, действие
// политики, тайминги и evidence. Одно решение модели может быть описано двумя
// событиями (engine и plugin) с общим trace_id/call_id; панель склеивает их и
// не считает двумя решениями. Сырой немаскированный ввод сюда не попадает:
// плагин маскирует до отправки, сервер дополнительно редактирует очевидные
// секреты и ограничивает размер текста перед записью на диск.
package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"layaengine/engine"
)

// WorkEvent — каноническая запись одного факта для /work.
type WorkEvent struct {
	SchemaVersion  int             `json:"schema_version"`
	EventID        string          `json:"event_id"`
	TraceID        string          `json:"trace_id"`
	CallID         string          `json:"call_id,omitempty"`
	BatchID        string          `json:"batch_id,omitempty"`
	ItemID         string          `json:"item_id,omitempty"`
	SessionID      string          `json:"session_id,omitempty"`
	Timestamp      string          `json:"timestamp"`
	Source         string          `json:"source"`
	Kind           string          `json:"kind"`
	Operation      string          `json:"operation,omitempty"`
	Status         string          `json:"status,omitempty"`
	Model          WorkModel       `json:"model,omitempty"`
	Input          WorkInput       `json:"input,omitempty"`
	Questions      []WorkQuestion  `json:"questions,omitempty"`
	ModelResult    json.RawMessage `json:"model_result,omitempty"`
	PolicyResult   json.RawMessage `json:"policy_result,omitempty"`
	Timing         WorkTiming      `json:"timing_ms,omitempty"`
	Evidence       json.RawMessage `json:"evidence,omitempty"`
	Labels         []WorkLabel     `json:"labels,omitempty"`
	PolicyVersion  string          `json:"policy_version,omitempty"`
	ModelAttempted bool            `json:"model_attempted,omitempty"`
	Demo           bool            `json:"demo,omitempty"`
	TextSaved      *bool           `json:"text_saved,omitempty"`
	Imported       bool            `json:"imported,omitempty"`
	Truncated      bool            `json:"truncated,omitempty"`
}

// WorkInput — три разные вещи: что дал агент, что реально отправлено в /decide
// после маскирования/сокращения клиентом и что увидела модель после токенизации
// и усечения движком. Текст после токенизации не восстанавливается искусственно:
// показываются счётчики токенов и флаг усечения.
type WorkInput struct {
	OriginalRedacted  string `json:"original_redacted,omitempty"`
	EffectiveRedacted string `json:"effective_redacted,omitempty"`
	Truncated         bool   `json:"truncated,omitempty"`
	OriginalChars     int    `json:"original_chars,omitempty"`
	EffectiveChars    int    `json:"effective_chars,omitempty"`
	TokenCount        int    `json:"token_count,omitempty"`
	ReceivedTokens    int    `json:"received_tokens,omitempty"`
	ProcessedTokens   int    `json:"processed_tokens,omitempty"`
	DroppedTokens     int    `json:"dropped_tokens,omitempty"`
	Chars             int    `json:"chars,omitempty"`
	StorageTruncated  bool   `json:"storage_truncated,omitempty"`
	TextSaved         *bool  `json:"text_saved,omitempty"`
}

// WorkQuestion — вопрос в том виде, в каком его увидела модель.
type WorkQuestion struct {
	ID           string          `json:"id"`
	Type         string          `json:"type"`
	Instructions string          `json:"instructions"`
	Options      []string        `json:"options,omitempty"`
	Criteria     json.RawMessage `json:"criteria,omitempty"`
}

// WorkModel — идентичность модели/чекпоинта.
type WorkModel struct {
	Checkpoint string `json:"checkpoint,omitempty"`
	Device     string `json:"device,omitempty"`
	ID         string `json:"id,omitempty"`
}

// WorkTiming — три независимых времени: подготовка бюджета, инференс, вся операция.
type WorkTiming struct {
	CountMS     float64 `json:"count,omitempty"`
	InferenceMS float64 `json:"inference,omitempty"`
	TotalMS     float64 `json:"total,omitempty"`
}

// WorkLabel — ручная разметка (эталон) конкретного вопроса. Метка привязана к
// call_id/question_id, а не ко всему событию: в одном вызове может быть несколько
// вопросов с разными правильными ответами.
type WorkLabel struct {
	CallID     string `json:"call_id"`
	QuestionID string `json:"question_id"`
	Expected   string `json:"expected"`
	Author     string `json:"author,omitempty"`
	Comment    string `json:"comment,omitempty"`
	Basis      string `json:"basis,omitempty"`
	TS         string `json:"ts,omitempty"`
}

// WorkStatus — состояние сервера для шапки /work.
type WorkStatus struct {
	Ready      bool            `json:"ready"`
	Device     string          `json:"device"`
	Checkpoint string          `json:"checkpoint"`
	Version    int             `json:"version"`
	UptimeS    int             `json:"uptime_s"`
	Model      ModelInfo       `json:"model"`
	Stats      engine.RunStats `json:"stats"`
}

// WorkStoreStats — состояние очереди телеметрии.
type WorkStoreStats struct {
	Enabled     bool   `json:"enabled"`
	Dropped     int64  `json:"dropped"`
	WriteErrors int64  `json:"write_errors"`
	Written     int64  `json:"written"`
	SpoolErrors int64  `json:"spool_errors"`
	Queue       int    `json:"queue"`
	DBPath      string `json:"db_path,omitempty"`
	SpoolDir    string `json:"spool_dir,omitempty"`
}

// WorkIndex — колонки, извлечённые из события для фильтров и графиков.
type WorkIndex struct {
	Selected     string
	PolicyAction string
	LayaCalled   bool
	Fallback     bool
	Truncated    bool
	InferenceMS  float64
	TotalMS      float64
}

const (
	// WorkSchemaVersion — версия схемы события. Увеличивается при несовместимом
	// изменении полей; старые записи остаются читаемыми.
	WorkSchemaVersion = 1
	// WorkPolicyVersion — версия правил/политики плагина и движка. Хранится в
	// каждой записи, чтобы после изменения порогов было видно, чем принималось
	// решение.
	WorkPolicyVersion = "work-v1"
	workSchemaVersion = WorkSchemaVersion
	workMaxJSONBytes  = 512 << 10
)

var (
	workIDRe = regexp.MustCompile(`[^A-Za-z0-9._:-]`)
	// Секреты редактируются до записи на диск. Это не замена маскированию в
	// плагине, а второй рубеж: прямой клиент /decide тоже может прислать токен.
	workSecretPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]{8,}`),
		regexp.MustCompile(`\bsk-[A-Za-z0-9]{16,}\b`),
		regexp.MustCompile(`\bghp_[A-Za-z0-9]{20,}\b`),
		regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`),
		regexp.MustCompile(`\bAKIA[A-Z0-9]{16}\b`),
		regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`),
		regexp.MustCompile(`(?i)(password|passwd|passphrase|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|client[_-]?secret|auth[_-]?token|cookie|session_id)\s*[=:]\s*["']?[^\s"'&]{3,}`),
	}
	workMask = "«СКРЫТО»"
)

// newWorkID генерирует короткий стабильный идентификатор с префиксом.
func newWorkID(prefix string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

// cleanWorkID оставляет только безопасные символы и ограничивает длину.
func cleanWorkID(v string, max int) string {
	v = strings.TrimSpace(v)
	v = workIDRe.ReplaceAllString(v, "")
	if max > 0 && len(v) > max {
		v = v[:max]
	}
	return v
}

// Normalize приводит событие к безопасному виду. maxInputChars ограничивает
// текст, который попадёт в SQLite; полный объём сохраняется в Chars.
func (e *WorkEvent) Normalize(maxInputChars int) error {
	if e.SchemaVersion == 0 {
		e.SchemaVersion = workSchemaVersion
	}
	if e.SchemaVersion != workSchemaVersion {
		return fmt.Errorf("schema_version %d не поддерживается", e.SchemaVersion)
	}
	if e.EventID == "" {
		e.EventID = newWorkID("evt")
	}
	e.EventID = cleanWorkID(e.EventID, 80)
	e.TraceID = cleanWorkID(e.TraceID, 80)
	if e.TraceID == "" {
		e.TraceID = newWorkID("tr")
	}
	e.CallID = cleanWorkID(e.CallID, 80)
	e.BatchID = cleanWorkID(e.BatchID, 80)
	e.ItemID = cleanWorkID(e.ItemID, 80)
	e.SessionID = cleanWorkID(e.SessionID, 120)

	if e.Timestamp == "" {
		e.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	} else if _, err := time.Parse(time.RFC3339Nano, e.Timestamp); err != nil {
		if _, err2 := time.Parse(time.RFC3339, e.Timestamp); err2 != nil {
			return fmt.Errorf("timestamp: %w", err)
		}
	}
	switch e.Source {
	case "engine", "plugin", "launcher", "import":
	case "":
		e.Source = "plugin"
	default:
		return fmt.Errorf("source %q не поддерживается", e.Source)
	}
	switch e.Kind {
	case "model_call", "policy", "tool_result", "agent_action", "check":
	case "":
		e.Kind = "policy"
	default:
		return fmt.Errorf("kind %q не поддерживается", e.Kind)
	}
	switch e.Status {
	case "completed", "error", "timeout", "fallback", "skipped", "running":
	case "":
		e.Status = "completed"
	default:
		e.Status = "completed"
	}
	e.Operation = cleanWorkID(e.Operation, 80)

	e.Input.OriginalRedacted = redactForStorage(e.Input.OriginalRedacted)
	e.Input.EffectiveRedacted = redactForStorage(e.Input.EffectiveRedacted)
	e.Input.OriginalChars = len([]rune(e.Input.OriginalRedacted))
	e.Input.EffectiveChars = len([]rune(e.Input.EffectiveRedacted))
	if e.Input.Chars == 0 {
		e.Input.Chars = e.Input.EffectiveChars
	}
	if e.Input.ReceivedTokens == 0 && e.Input.TokenCount > 0 {
		e.Input.ReceivedTokens = e.Input.TokenCount
	}
	if e.Input.ProcessedTokens == 0 && e.Input.ReceivedTokens > e.Input.DroppedTokens {
		e.Input.ProcessedTokens = e.Input.ReceivedTokens - e.Input.DroppedTokens
	}
	if e.PolicyVersion == "" {
		e.PolicyVersion = WorkPolicyVersion
	}
	if maxInputChars > 0 {
		var t1, t2 bool
		e.Input.OriginalRedacted, t1 = truncateRunes(e.Input.OriginalRedacted, maxInputChars)
		e.Input.EffectiveRedacted, t2 = truncateRunes(e.Input.EffectiveRedacted, maxInputChars)
		e.Input.StorageTruncated = t1 || t2
	}
	e.Truncated = e.Truncated || e.Input.Truncated
	if e.TextSaved == nil {
		v := e.Input.EffectiveRedacted != "" || e.Input.OriginalRedacted != ""
		e.TextSaved = &v
	}
	e.Input.TextSaved = e.TextSaved

	if len(e.Questions) > 32 {
		e.Questions = e.Questions[:32]
	}
	for i := range e.Questions {
		e.Questions[i].ID = cleanWorkID(e.Questions[i].ID, 80)
		e.Questions[i].Type = cleanWorkID(e.Questions[i].Type, 32)
		e.Questions[i].Instructions = redactForStorage(e.Questions[i].Instructions)
		if len(e.Questions[i].Options) > 64 {
			e.Questions[i].Options = e.Questions[i].Options[:64]
		}
		for j := range e.Questions[i].Options {
			e.Questions[i].Options[j] = redactForStorage(e.Questions[i].Options[j])
		}
		e.Questions[i].Criteria = clampRawJSON(e.Questions[i].Criteria)
	}
	e.ModelResult = clampRawJSON(e.ModelResult)
	e.PolicyResult = clampRawJSON(e.PolicyResult)
	e.Evidence = clampRawJSON(e.Evidence)
	if e.Model.Checkpoint != "" {
		e.Model.Checkpoint = cleanWorkID(e.Model.Checkpoint, 120)
	}
	if e.Model.Device != "" {
		e.Model.Device = cleanWorkID(e.Model.Device, 32)
	}
	return nil
}

// Index извлекает то, по чему панель фильтрует и строит графики.
func (e *WorkEvent) Index() WorkIndex {
	idx := WorkIndex{
		LayaCalled:  e.Kind == "model_call",
		Fallback:    e.Status == "fallback",
		Truncated:   e.Truncated || e.Input.Truncated,
		InferenceMS: e.Timing.InferenceMS,
		TotalMS:     e.Timing.TotalMS,
	}
	var mr map[string]any
	if len(e.ModelResult) > 0 && json.Unmarshal(e.ModelResult, &mr) == nil {
		if v, ok := mr["selected"].(string); ok {
			idx.Selected = v
		}
		if idx.Selected == "" {
			idx.Selected = selectedFromAnswers(mr)
		}
		if b, ok := mr["budget"].(map[string]any); ok {
			if t, ok := b["state_truncated"].(bool); ok && t {
				idx.Truncated = true
			}
		}
		if im, ok := number(mr["elapsed_ms"]); ok && idx.InferenceMS == 0 {
			idx.InferenceMS = im
		}
	}
	var pr map[string]any
	if len(e.PolicyResult) > 0 && json.Unmarshal(e.PolicyResult, &pr) == nil {
		if v, ok := pr["action"].(string); ok {
			idx.PolicyAction = v
		}
		if v, ok := pr["laya_called"].(bool); ok {
			idx.LayaCalled = v
		}
		if v, ok := pr["fallback"].(bool); ok && v {
			idx.Fallback = true
		}
		if v, ok := pr["truncated"].(bool); ok && v {
			idx.Truncated = true
		}
	}
	if e.Kind == "policy" && idx.PolicyAction == "" {
		idx.PolicyAction = e.Status
	}
	return idx
}

func selectedFromAnswers(m map[string]any) string {
	answers, ok := m["answers"].(map[string]any)
	if !ok {
		return ""
	}
	order, _ := m["order"].([]any)
	for _, id := range order {
		key, _ := id.(string)
		if v, ok := answers[key]; ok {
			if s := selectedFromAnswer(v); s != "" {
				return s
			}
		}
	}
	for _, v := range answers {
		if s := selectedFromAnswer(v); s != "" {
			return s
		}
	}
	return ""
}

func selectedFromAnswer(v any) string {
	a, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	if c, ok := a["choice"].(string); ok && c != "" {
		return c
	}
	if s, ok := number(a["score"]); ok {
		return strconv.FormatFloat(s, 'f', -1, 64)
	}
	if n, ok := number(a["noul"]); ok {
		return strconv.FormatFloat(n, 'f', 3, 64)
	}
	return ""
}

// selectedForQuestion достаёт ответ именно на question_id. Для старых записей
// без answers используется общий selected (один вопрос).
func selectedForQuestion(raw json.RawMessage, qid string) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return "", false
	}
	if answers, ok := m["answers"].(map[string]any); ok {
		if v, ok := answers[qid]; ok {
			if s := selectedFromAnswer(v); s != "" {
				return s, true
			}
		}
	}
	if scores, ok := m["scores"].(map[string]any); ok {
		if v, ok := scores[qid]; ok {
			if s := selectedFromAnswer(v); s != "" {
				return s, true
			}
		}
	}
	if byQ, ok := m["selected_by_question"].(map[string]any); ok {
		if s, ok := byQ[qid].(string); ok && s != "" {
			return s, true
		}
	}
	if s, ok := m["selected"].(string); ok && s != "" {
		return s, true
	}
	return "", false
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func clampRawJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	if len(raw) > workMaxJSONBytes {
		marker, _ := json.Marshal(map[string]any{
			"storage_truncated": true,
			"original_bytes":    len(raw),
			"note":              "событие больше лимита хранения; полный ответ не сохранён",
		})
		return marker
	}
	return raw
}

// renderQuestions переводит вопросы движка в форму для хранения и UI.
func renderQuestions(ids []string, questions []engine.Question) []WorkQuestion {
	out := make([]WorkQuestion, 0, len(questions))
	for i, q := range questions {
		id := ""
		if i < len(ids) {
			id = ids[i]
		}
		options, _ := engine.RenderOptions(q)
		wq := WorkQuestion{
			ID:           id,
			Type:         q.Type.Name(),
			Instructions: q.Instructions,
			Options:      options,
			Criteria:     q.Criteria,
		}
		out = append(out, wq)
	}
	return out
}

// redactForStorage редактирует очевидные секреты и ограничивает размер.
func redactForStorage(s string) string {
	if s == "" {
		return s
	}
	for _, re := range workSecretPatterns {
		s = re.ReplaceAllStringFunc(s, func(m string) string { return redactMatch(m) })
	}
	return s
}

func redactMatch(m string) string {
	lower := strings.ToLower(m)
	for _, key := range []string{"bearer", "password", "passwd", "passphrase", "secret", "token", "api_key", "api-key", "access_key", "private_key", "client_secret", "auth_token", "cookie", "session_id"} {
		i := strings.Index(lower, key)
		if i < 0 {
			continue
		}
		rest := m[i+len(key):]
		if j := strings.IndexAny(rest, "=: "); j >= 0 {
			prefix := strings.TrimRight(m[:i+len(key)+j+1], " ")
			return prefix + " " + workMask
		}
		return m[:i+len(key)] + " " + workMask
	}
	return workMask
}

// truncateRunes обрезает середину, сохраняя начало и конец.
func truncateRunes(s string, max int) (string, bool) {
	if max <= 0 {
		return s, false
	}
	r := []rune(s)
	if len(r) <= max {
		return s, false
	}
	head := max * 3 / 4
	tail := max - head - 48
	if tail < 0 {
		tail = 0
	}
	removed := len(r) - head - tail
	out := string(r[:head]) + "\n…[сокращено для хранения " + strconv.Itoa(removed) + " симв.]…\n" + string(r[len(r)-tail:])
	return out, true
}
