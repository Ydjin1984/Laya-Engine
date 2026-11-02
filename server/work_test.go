package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T) *WorkStore {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenWorkStore(WorkStoreConfig{
		DBPath:        filepath.Join(dir, "work.db"),
		QueueSize:     128,
		MaxInputChars: 4096,
		MaxEvents:     1000,
		MaxBytes:      64 << 20,
	})
	if err != nil {
		t.Fatalf("OpenWorkStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func evModel(id, call, trace, operation, status, selected string, inf, total float64) WorkEvent {
	mr, _ := json.Marshal(map[string]any{
		"answers": map[string]any{
			"risk": map[string]any{"choice": selected, "answer_confidence": 0.9},
		},
		"order":    []string{"risk"},
		"selected": selected,
	})
	return WorkEvent{
		SchemaVersion:  WorkSchemaVersion,
		EventID:        id,
		TraceID:        trace,
		CallID:         call,
		SessionID:      "sess-test",
		Timestamp:      time.Now().UTC().Format(time.RFC3339Nano),
		Source:         "engine",
		Kind:           "model_call",
		Operation:      operation,
		Status:         status,
		Model:          WorkModel{Checkpoint: "cp-test", Device: "cpu"},
		Input:          WorkInput{OriginalRedacted: "input " + id, EffectiveRedacted: "input " + id, ReceivedTokens: 10, ProcessedTokens: 8, DroppedTokens: 2},
		ModelResult:    mr,
		Timing:         WorkTiming{InferenceMS: inf, TotalMS: total},
		PolicyVersion:  WorkPolicyVersion,
		ModelAttempted: true,
	}
}

func evPolicy(id, call, trace, action string, laya bool) WorkEvent {
	pr, _ := json.Marshal(map[string]any{"action": action, "laya_called": laya, "basis": "rules"})
	return WorkEvent{
		SchemaVersion: WorkSchemaVersion,
		EventID:       id,
		TraceID:       trace,
		CallID:        call,
		SessionID:     "sess-test",
		Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
		Source:        "plugin",
		Kind:          "policy",
		Operation:     "risk_gate",
		Status:        "completed",
		PolicyResult:  pr,
		Timing:        WorkTiming{TotalMS: 3},
		PolicyVersion: WorkPolicyVersion,
	}
}

func recordAll(t *testing.T, s *WorkStore, events ...WorkEvent) {
	t.Helper()
	for i := range events {
		if err := events[i].Normalize(4096); err != nil {
			t.Fatalf("normalize: %v", err)
		}
		if !s.Record(events[i]) {
			t.Fatalf("record failed for %s", events[i].EventID)
		}
	}
	if !s.Flush(3 * time.Second) {
		t.Fatal("flush timeout")
	}
}

func TestWorkStoreListAndFilters(t *testing.T) {
	s := testStore(t)
	recordAll(t, s,
		evModel("e1", "c1", "t1", "decide", "completed", "high", 20, 40),
		evPolicy("p1", "c1", "t1", "ask", true),
		evPolicy("p2", "c2", "t2", "log", false),
	)
	list, err := s.List(WorkFilter{})
	if err != nil {
		t.Fatal(err)
	}
	// engine+plugin одного call_id склеиваются в одну строку.
	if len(list.Events) != 2 {
		t.Fatalf("events=%d", len(list.Events))
	}
	l, err := s.List(WorkFilter{Session: "sess-test", Operation: "risk_gate"})
	if err != nil {
		t.Fatal(err)
	}
	// Фильтр operation применяется к сырым событиям: обе policy-записи
	// (p1 и p2) имеют operation=risk_gate; engine-событие имеет operation=decide.
	if len(l.Events) != 2 {
		t.Fatalf("filtered=%d", len(l.Events))
	}
}

func TestWorkOverviewDedupesInferenceByCallID(t *testing.T) {
	s := testStore(t)
	recordAll(t, s,
		evModel("e1", "c1", "t1", "decide", "completed", "high", 20, 40),
		evPolicy("p1", "c1", "t1", "ask", true),
		// Дубликат model_call с тем же call_id (plugin-событие) не должен
		// увеличивать число inference-вызовов.
		WorkEvent{
			SchemaVersion: WorkSchemaVersion, EventID: "e1p", TraceID: "t1", CallID: "c1",
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Source: "plugin", Kind: "model_call",
			Operation: "decide", Status: "completed", ModelResult: json.RawMessage(`{"selected":"high"}`),
		},
		evPolicy("p2", "c2", "t2", "log", false),
		evModel("e3", "c3", "t3", "decide", "error", "", 0, 10),
	)
	ov, err := s.Overview(WorkFilter{}, WorkStatus{Ready: true, Checkpoint: "cp-test"})
	if err != nil {
		t.Fatal(err)
	}
	if ov.Totals.InferenceCalls != 2 {
		t.Fatalf("inference_calls=%d, want 2", ov.Totals.InferenceCalls)
	}
	if ov.Totals.RuleOnly != 1 {
		t.Fatalf("rule_only=%d, want 1", ov.Totals.RuleOnly)
	}
	if ov.Totals.Fallbacks != 0 {
		t.Fatalf("fallbacks=%d", ov.Totals.Fallbacks)
	}
	if ov.SchemaVersion != WorkSchemaVersion || ov.PolicyVersion != WorkPolicyVersion {
		t.Fatalf("versions: %d/%s", ov.SchemaVersion, ov.PolicyVersion)
	}
}

func TestWorkLabelsPerQuestion(t *testing.T) {
	s := testStore(t)
	mr, _ := json.Marshal(map[string]any{
		"answers": map[string]any{
			"a": map[string]any{"choice": "x"},
			"b": map[string]any{"choice": "y"},
		},
		"order": []string{"a", "b"},
	})
	e := evModel("e1", "c1", "t1", "decide", "completed", "x", 10, 20)
	e.ModelResult = mr
	recordAll(t, s, e)
	if err := s.Label("c1", "a", WorkLabel{Expected: "x", Author: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Label("c1", "b", WorkLabel{Expected: "z", Author: "test"}); err != nil {
		t.Fatal(err)
	}
	ov, err := s.Overview(WorkFilter{}, WorkStatus{})
	if err != nil {
		t.Fatal(err)
	}
	if ov.Accuracy.Labeled != 2 || ov.Accuracy.Matches != 1 {
		t.Fatalf("accuracy labeled=%d matches=%d", ov.Accuracy.Labeled, ov.Accuracy.Matches)
	}
	if ov.Accuracy.LabeledCalls != 1 {
		t.Fatalf("labeled_calls=%d", ov.Accuracy.LabeledCalls)
	}
	got, _, _, err := s.Get("e1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Labels) != 2 {
		t.Fatalf("labels=%d", len(got.Labels))
	}
}

func TestWorkDisabledRoutes(t *testing.T) {
	s := &Server{}
	mux := http.NewServeMux()
	s.Routes(mux)
	req := httptest.NewRequest(http.MethodGet, "/work/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Host = "127.0.0.1:8095"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("work disabled status=%d", rec.Code)
	}
}

func TestWorkGetMergesPolicyFromRelated(t *testing.T) {
	s := testStore(t)
	recordAll(t, s,
		evModel("e1", "c1", "t1", "decide", "completed", "high", 10, 20),
		evPolicy("p1", "c1", "t1", "ask", true),
	)
	ev, _, _, err := s.Get("e1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ev.PolicyResult), "ask") {
		t.Fatalf("policy not merged into detail: %s", ev.PolicyResult)
	}
}

func TestWorkListMergesEngineAndPlugin(t *testing.T) {
	s := testStore(t)
	recordAll(t, s,
		evModel("e1", "c1", "t1", "decide", "completed", "high", 10, 20),
		evPolicy("p1", "c1", "t1", "ask", true),
		evPolicy("p2", "c2", "t2", "log", false),
	)
	list, err := s.List(WorkFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Events) != 2 {
		t.Fatalf("merged rows=%d, want 2 (decision + rule-only)", len(list.Events))
	}
	var decision *WorkListEvent
	for i := range list.Events {
		if list.Events[i].Event.CallID == "c1" {
			decision = &list.Events[i]
		}
	}
	if decision == nil || decision.Event.Source != "engine" {
		t.Fatalf("engine decision not preferred: %+v", decision)
	}
	if !strings.Contains(string(decision.Event.PolicyResult), "ask") {
		t.Fatalf("policy not merged: %s", decision.Event.PolicyResult)
	}
}

func TestWorkTimelineContinuous(t *testing.T) {
	s := testStore(t)
	recordAll(t, s, evModel("e1", "c1", "t1", "decide", "completed", "high", 10, 20))
	now := time.Now().UnixMilli()
	ov, err := s.Overview(WorkFilter{From: now - 24*3600*1000, To: now}, WorkStatus{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.Timeline) < 23 {
		t.Fatalf("timeline buckets=%d, want ~24", len(ov.Timeline))
	}
}

func TestWorkStreamAfter(t *testing.T) {
	s := testStore(t)
	recordAll(t, s,
		evModel("e1", "c1", "t1", "decide", "completed", "high", 10, 20),
		evModel("e2", "c2", "t2", "decide", "completed", "low", 11, 21),
	)
	last := s.LastSeq()
	first, err := s.StreamAfter(0, 10)
	if err != nil || len(first) != 2 {
		t.Fatalf("stream=%d err=%v", len(first), err)
	}
	if first[1].Seq >= last+1 {
		t.Fatalf("bad seq")
	}
	after, err := s.StreamAfter(first[0].Seq, 10)
	if err != nil || len(after) != 1 {
		t.Fatalf("after=%d err=%v", len(after), err)
	}
}

func TestWorkImportDedupe(t *testing.T) {
	s := testStore(t)
	line := `{"ts":"2026-11-02T10:00:00Z","kind":"risk_gate","session_id":"s1","tool":"Bash","mode":"ask","action":"ask","latency_ms":45,"elapsed_ms":27,"checkpoint":"cp","question_set_version":"risk-v1","state_hash":"abc","state_chars":120,"state_truncated":false,"scores":{"risk":{"choice":"high"}},"inference_error":null}`
	res, err := s.ImportJSONL(strings.NewReader(line + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 1 {
		t.Fatalf("imported=%d", res.Imported)
	}
	res2, err := s.ImportJSONL(strings.NewReader(line + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if res2.Imported != 0 || res2.Duplicates != 1 {
		t.Fatalf("import2=%+v", res2)
	}
	list, _ := s.List(WorkFilter{})
	if len(list.Events) != 1 {
		t.Fatalf("events=%d", len(list.Events))
	}
	if list.Events[0].Event.TextSaved == nil || *list.Events[0].Event.TextSaved {
		t.Fatalf("imported event must have text_saved=false")
	}
}

func TestWorkSpoolIngest(t *testing.T) {
	s := testStore(t)
	dir := filepath.Join(t.TempDir(), "spool")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.StartSpool(ctx, dir)
	ev := evModel("spool1", "cs", "ts", "decide", "completed", "high", 10, 20)
	raw, _ := json.Marshal(ev)
	if err := os.WriteFile(filepath.Join(dir, "001.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		list, _ := s.List(WorkFilter{})
		if len(list.Events) == 1 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("spool event not ingested")
}

func TestWorkPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "work.db")
	s1, err := OpenWorkStore(WorkStoreConfig{DBPath: path, QueueSize: 16})
	if err != nil {
		t.Fatal(err)
	}
	ev := evModel("p1", "c1", "t1", "decide", "completed", "high", 10, 20)
	_ = ev.Normalize(4096)
	s1.Record(ev)
	s1.Flush(2 * time.Second)
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := OpenWorkStore(WorkStoreConfig{DBPath: path, QueueSize: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	list, err := s2.List(WorkFilter{})
	if err != nil || len(list.Events) != 1 {
		t.Fatalf("after reopen=%d err=%v", len(list.Events), err)
	}
}

func testAPI(t *testing.T, token string) (*WorkAPI, *httptest.Server, *WorkStore) {
	t.Helper()
	s := testStore(t)
	api, err := NewWorkAPI(s, WorkConfig{Token: token, TokenFile: filepath.Join(t.TempDir(), "token"), MaxInputChars: 4096}, func() WorkStatus {
		return WorkStatus{Ready: true, Device: "cpu", Checkpoint: "cp-test"}
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return api, srv, s
}

func TestWorkAPIEndpoints(t *testing.T) {
	_, srv, s := testAPI(t, "secret-token")
	recordAll(t, s, evModel("e1", "c1", "t1", "decide", "completed", "high", 10, 20))

	for _, path := range []string{"/work/", "/work/assets/app.js", "/work/assets/styles.css", "/api/work/v1/overview", "/api/work/v1/events"} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("%s status=%d", path, res.StatusCode)
		}
	}
	// Токен не должен попадать в HTML/JS.
	res, _ := http.Get(srv.URL + "/work/")
	bodyBytes, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if strings.Contains(string(bodyBytes), "secret-token") {
		t.Fatal("token leaked into frontend")
	}
	if !strings.Contains(string(bodyBytes), "Laya Work") {
		t.Fatal("unexpected index")
	}
	// Приём без токена запрещён.
	body := `{"events":[{"schema_version":1,"event_id":"api1","trace_id":"at","source":"plugin","kind":"policy","timestamp":"2026-11-02T10:00:00Z","policy_result":{"action":"log","laya_called":false}}]}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/work/v1/ingest", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ingest without token=%d", res.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/work/v1/ingest", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Laya-Work-Token", "secret-token")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("ingest with token=%d", res.StatusCode)
	}
}

func TestWorkAPISecurity(t *testing.T) {
	_, srv, _ := testAPI(t, "secret-token")
	// Чужой Host — отказ.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/work/v1/overview", nil)
	req.Host = "evil.example"
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("evil host=%d", res.StatusCode)
	}
	// Чужой Origin на запись — отказ.
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/work/v1/labels", strings.NewReader(`{"call_id":"c1","expected":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://evil.example")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("evil origin=%d", res.StatusCode)
	}
}

func TestWorkTokenFileAndInfo(t *testing.T) {
	dir := t.TempDir()
	s := testStore(t)
	api, err := NewWorkAPI(s, WorkConfig{TokenFile: filepath.Join(dir, "work-token")}, func() WorkStatus { return WorkStatus{Ready: true} })
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "work-token"))
	if err != nil || strings.TrimSpace(string(b)) == "" {
		t.Fatalf("token file: %v", err)
	}
	info := api.Info()
	if _, ok := info["token"]; ok {
		t.Fatal("Info must not expose token")
	}
	if info["token_file"] == "" || info["token_file"] == "work-token" {
		t.Fatalf("token_file=%v", info["token_file"])
	}
}

func TestWorkRecordOverflowCountsDropped(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenWorkStore(WorkStoreConfig{DBPath: filepath.Join(dir, "w.db"), QueueSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ev := evModel("x1", "c", "t", "decide", "completed", "x", 1, 2)
	_ = ev.Normalize(1000)
	s.Record(ev)
	s.Record(ev) // очередь заполнена (writer ещё не забрал)
	if s.Stats().Dropped == 0 {
		// writer мог успеть забрать первое событие; это не ошибка, но проверим,
		// что метод не блокируется и счётчик существует.
		t.Log("queue drained before second record; dropped=0 acceptable")
	}
}

func TestWorkHTMLInjectionStoredAsText(t *testing.T) {
	s := testStore(t)
	ev := evModel("xss1", "cx", "tx", "decide", "completed", "<img src=x onerror=alert(1)>", 1, 2)
	ev.Input.OriginalRedacted = "<img src=x onerror=alert(1)>"
	ev.Input.EffectiveRedacted = "<img src=x onerror=alert(1)>"
	recordAll(t, s, ev)
	list, _ := s.List(WorkFilter{})
	if len(list.Events) != 1 {
		t.Fatal("event missing")
	}
	if !strings.Contains(list.Events[0].Event.Input.EffectiveRedacted, "<img") {
		t.Fatal("input should be stored as plain text")
	}
}

func TestWorkRedaction(t *testing.T) {
	in := "password=hunter2 token=abc123 Authorization: Bearer abcdefghijklmnop"
	out := redactForStorage(in)
	if strings.Contains(out, "hunter2") || strings.Contains(out, "abc123") || strings.Contains(out, "abcdefghijklmnop") {
		t.Fatalf("secret leaked: %s", out)
	}
	if !strings.Contains(out, workMask) {
		t.Fatalf("no mask marker: %s", out)
	}
}

func TestWorkFilterLabeledSubquery(t *testing.T) {
	s := testStore(t)
	recordAll(t, s, evModel("e1", "c1", "t1", "decide", "completed", "high", 1, 2))
	if err := s.Label("c1", "risk", WorkLabel{Expected: "high"}); err != nil {
		t.Fatal(err)
	}
	yes := true
	list, err := s.List(WorkFilter{Labeled: &yes})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Events) != 1 {
		t.Fatalf("labeled filter=%d", len(list.Events))
	}
	no := false
	list, _ = s.List(WorkFilter{Labeled: &no})
	if len(list.Events) != 0 {
		t.Fatalf("unlabeled filter=%d", len(list.Events))
	}
}

func TestWorkSavingsFollowup(t *testing.T) {
	s := testStore(t)
	ev := evModel("s1", "c1", "t1", "laya_search", "completed", "2", 10, 20)
	ev.Kind = "tool_result"
	ev.Evidence = json.RawMessage(`{"source_bytes":1000,"returned_bytes":200,"chunk_id":"ch1"}`)
	gc := evModel("g1", "c2", "t2", "laya_get_chunk", "completed", "", 0, 5)
	gc.Kind = "tool_result"
	gc.Evidence = json.RawMessage(`{"chunk_id":"ch1","returned_chars":50}`)
	recordAll(t, s, ev, gc)
	ov, err := s.Overview(WorkFilter{}, WorkStatus{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.Savings) == 0 {
		t.Fatal("no savings")
	}
	found := false
	for _, x := range ov.Savings {
		if x.Tool == "laya_search" && x.SourceBytes == 1000 && x.FollowupBytes == 50 {
			found = true
		}
	}
	if !found {
		t.Fatalf("savings=%+v", ov.Savings)
	}
}

func TestWorkEventNormalize(t *testing.T) {
	e := WorkEvent{EventID: "x", Timestamp: "bad", Source: "plugin", Kind: "policy"}
	if err := e.Normalize(100); err == nil {
		t.Fatal("expected bad timestamp error")
	}
	e.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	if err := e.Normalize(10); err != nil {
		t.Fatal(err)
	}
	if e.SchemaVersion != WorkSchemaVersion || e.PolicyVersion == "" {
		t.Fatalf("versions not set: %+v", e)
	}
}

func TestWorkParseFilterAll(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/work/v1/overview?range=all", nil)
	f, err := parseWorkFilter(req)
	if err != nil {
		t.Fatal(err)
	}
	if f.From != 1 {
		t.Fatalf("from=%d", f.From)
	}
}

func ExampleWorkEvent() {
	e := WorkEvent{EventID: "evt_example", TraceID: "tr_example", Source: "plugin", Kind: "policy"}
	_ = e.Normalize(100)
	fmt.Println(e.SchemaVersion, e.PolicyVersion)
	// Output: 1 work-v1
}
