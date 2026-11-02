// Package server — запись фактических вызовов модели в панель /work.
//
// Engine-событие — источник истины по инференсу: вход, вопросы, ответы, бюджет,
// время. Plugin-событие добавляет исходный вход и политику; они связываются по
// trace_id/call_id. Запись не блокирует /decide: только неблокирующая очередь.
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"layaengine/engine"
)

type workRequestMeta struct {
	TraceID   string
	CallID    string
	SessionID string
	Operation string
	Tool      string
	ToolUseID string
	StartedAt time.Time
}

func workMetaFromRequest(r *http.Request, fallbackOperation string) workRequestMeta {
	op := cleanWorkID(r.Header.Get("X-Laya-Operation"), 80)
	if op == "" {
		op = fallbackOperation
	}
	trace := cleanWorkID(r.Header.Get("X-Laya-Trace-Id"), 80)
	if trace == "" {
		trace = newWorkID("tr")
	}
	call := cleanWorkID(r.Header.Get("X-Laya-Call-Id"), 80)
	if call == "" {
		call = newWorkID("call")
	}
	return workRequestMeta{
		TraceID:   trace,
		CallID:    call,
		SessionID: cleanWorkID(r.Header.Get("X-Laya-Session-Id"), 120),
		Operation: op,
		Tool:      cleanWorkID(r.Header.Get("X-Laya-Tool"), 80),
		ToolUseID: cleanWorkID(r.Header.Get("X-Laya-Tool-Use-Id"), 80),
		StartedAt: time.Now(),
	}
}

func (s *Server) recordEngineCall(meta workRequestMeta, state string, ids []string, questions []engine.Question, res *engine.Result, started time.Time, requestMaxLen int) {
	if s.work == nil || res == nil {
		return
	}
	answers := make(map[string]engine.Answer, len(ids))
	byQuestion := make(map[string]string, len(ids))
	for i, id := range ids {
		if i >= len(res.Answers) {
			break
		}
		a := res.Answers[i]
		a.ID = id
		answers[id] = a
		byQuestion[id] = answerSelected(a)
	}
	selected := ""
	for _, id := range ids {
		if byQuestion[id] != "" {
			selected = byQuestion[id]
			break
		}
	}
	modelResult := map[string]any{
		"answers":              answers,
		"order":                ids,
		"selected":             selected,
		"selected_by_question": byQuestion,
		"usage": map[string]any{
			"input_tokens":  res.InputTokens,
			"output_tokens": 0,
		},
		"budget": map[string]any{
			"state_tokens":      res.StateTokens,
			"state_dropped_max": res.StateDroppedMax,
			"state_truncated":   res.Truncated,
			"max_len":           s.maxLenFor(requestMaxLen),
		},
		"elapsed_ms": res.ElapsedMs,
	}
	raw, _ := json.Marshal(modelResult)
	processed := res.StateTokens - res.StateDroppedMax
	if processed < 0 {
		processed = 0
	}
	ev := WorkEvent{
		SchemaVersion: WorkSchemaVersion,
		EventID:       newWorkID("eng"),
		TraceID:       meta.TraceID,
		CallID:        meta.CallID,
		SessionID:     meta.SessionID,
		Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
		Source:        "engine",
		Kind:          "model_call",
		Operation:     meta.Operation,
		Status:        "completed",
		Model:         WorkModel{Checkpoint: s.checkpoint.ID(), Device: s.model.Device()},
		Input: WorkInput{
			OriginalRedacted:  state,
			EffectiveRedacted: state,
			ReceivedTokens:    res.StateTokens,
			ProcessedTokens:   processed,
			DroppedTokens:     res.StateDroppedMax,
			Truncated:         res.Truncated,
			Chars:             len([]rune(state)),
		},
		Questions:      renderQuestions(ids, questions),
		ModelResult:    raw,
		Timing:         WorkTiming{InferenceMS: res.ElapsedMs, TotalMS: workMillisSince(started)},
		PolicyVersion:  WorkPolicyVersion,
		ModelAttempted: true,
		Truncated:      res.Truncated,
	}
	if err := ev.Normalize(s.workMaxInputChars); err != nil {
		return
	}
	s.work.Record(ev)
}

func (s *Server) recordEngineError(meta workRequestMeta, state string, ids []string, questions []engine.Question, started time.Time, code string, attempted bool) {
	if s.work == nil {
		return
	}
	raw, _ := json.Marshal(map[string]any{
		"error":    code,
		"selected": "",
	})
	ev := WorkEvent{
		SchemaVersion:  WorkSchemaVersion,
		EventID:        newWorkID("eng"),
		TraceID:        meta.TraceID,
		CallID:         meta.CallID,
		SessionID:      meta.SessionID,
		Timestamp:      time.Now().UTC().Format(time.RFC3339Nano),
		Source:         "engine",
		Kind:           "model_call",
		Operation:      meta.Operation,
		Status:         "error",
		Model:          WorkModel{Checkpoint: s.checkpoint.ID(), Device: s.model.Device()},
		Input:          WorkInput{OriginalRedacted: state, EffectiveRedacted: state, Chars: len([]rune(state))},
		Questions:      renderQuestions(ids, questions),
		ModelResult:    raw,
		Timing:         WorkTiming{TotalMS: workMillisSince(started)},
		PolicyVersion:  WorkPolicyVersion,
		ModelAttempted: attempted,
	}
	if err := ev.Normalize(s.workMaxInputChars); err != nil {
		return
	}
	s.work.Record(ev)
}

func answerSelected(a engine.Answer) string {
	switch a.Type {
	case "choice":
		return a.Choice
	case "score":
		return strconv.FormatFloat(a.Score, 'f', -1, 64)
	case "noul":
		return strconv.FormatFloat(a.Noul, 'f', 3, 64)
	}
	return ""
}

func workMillisSince(t time.Time) float64 {
	return float64(time.Since(t).Microseconds()) / 1000.0
}

func (m workRequestMeta) String() string {
	return fmt.Sprintf("%s/%s", m.TraceID, m.CallID)
}
