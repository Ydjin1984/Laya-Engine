// Package server — HTTP-слой панели Laya Work.
//
// Work-маршруты доступны только процессаv на loopback (RemoteAddr) и только с
// loopback-Host: внешний клиент не получит доступ, даже если сервер случайно
// слушает 0.0.0.0. Запись событий плагина защищена локальным токеном; токен
// никогда не отдаётся во frontend, не пишется в лог и не передаётся аргументом
// командной строки.
package server

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

//go:embed web
var workWebFS embed.FS

// WorkConfig — настройки HTTP-слоя.
type WorkConfig struct {
	Token         string
	TokenFile     string
	MaxInputChars int
	Demo          bool
	BindAddr      string
}

// WorkAPI — маршруты /work и /api/work/v1/*.
type WorkAPI struct {
	store   *WorkStore
	cfg     WorkConfig
	status  func() WorkStatus
	token   string
	assets  fs.FS
	assetFS fs.FS
	index   []byte
}

// NewWorkAPI создаёт API, читает/создаёт локальный токен и загружает embed.
func NewWorkAPI(store *WorkStore, cfg WorkConfig, status func() WorkStatus) (*WorkAPI, error) {
	a := &WorkAPI{store: store, cfg: cfg, status: status}
	if cfg.TokenFile == "" {
		cfg.TokenFile = filepath.Join("data", "work-token")
	}
	a.cfg.TokenFile = cfg.TokenFile
	if cfg.Token != "" {
		a.token = strings.TrimSpace(cfg.Token)
	} else {
		token, err := loadOrCreateToken(cfg.TokenFile)
		if err != nil {
			// Панель чтения остаётся доступной; приём событий будет отвечать 503.
			a.token = ""
			a.cfg.Token = ""
			_ = err
		} else {
			a.token = token
		}
	}
	assets, err := fs.Sub(workWebFS, "web")
	if err != nil {
		return nil, err
	}
	a.assets = assets
	assetFS, err := fs.Sub(assets, "assets")
	if err != nil {
		return nil, err
	}
	a.assetFS = assetFS
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		return nil, err
	}
	a.index = index
	return a, nil
}

// TokenPath — абсолютный путь к файлу токена (для /info; сам токен не отдаётся).
func (a *WorkAPI) TokenPath() string {
	if a == nil {
		return ""
	}
	p, err := filepath.Abs(a.cfg.TokenFile)
	if err != nil {
		return a.cfg.TokenFile
	}
	return p
}

// Info — публичная (loopback) информация о Work для /info.
func (a *WorkAPI) Info() map[string]any {
	if a == nil {
		return nil
	}
	st := a.store.Stats()
	return map[string]any{
		"enabled":        true,
		"url":            "/work/",
		"api":            "/api/work/v1",
		"spool_dir":      st.SpoolDir,
		"token_file":     a.TokenPath(),
		"schema_version": WorkSchemaVersion,
		"policy_version": WorkPolicyVersion,
		"dropped":        st.Dropped,
		"write_errors":   st.WriteErrors,
	}
}

// Register регистрирует маршруты в существующем mux.
func (a *WorkAPI) Register(mux *http.ServeMux) {
	mux.HandleFunc("/work", a.guard(a.handleWorkRedirect, guardRead))
	mux.HandleFunc("/work/", a.guard(a.handleWorkIndex, guardRead))
	mux.HandleFunc("/work/assets/", a.guard(a.handleWorkAsset, guardRead))
	mux.HandleFunc("/api/work/v1/overview", a.guard(a.handleOverview, guardRead))
	mux.HandleFunc("/api/work/v1/events", a.guard(a.handleEvents, guardRead))
	mux.HandleFunc("/api/work/v1/events/", a.guard(a.handleEvent, guardRead))
	mux.HandleFunc("/api/work/v1/stream", a.guard(a.handleStream, guardRead))
	mux.HandleFunc("/api/work/v1/labels", a.guard(a.handleLabels, guardWrite))
	mux.HandleFunc("/api/work/v1/export", a.guard(a.handleExport, guardRead))
	mux.HandleFunc("/api/work/v1/import", a.guard(a.handleImport, guardWrite))
	mux.HandleFunc("/api/work/v1/ingest", a.guard(a.handleIngest, guardToken))
}

type guardMode int

const (
	guardRead guardMode = iota
	guardWrite
	guardToken
)

func (a *WorkAPI) guard(next http.HandlerFunc, mode guardMode) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackRemote(r.RemoteAddr) || !isLoopbackHost(r.Host) {
			writeError(w, http.StatusForbidden, "forbidden", "Work доступен только с loopback")
			return
		}
		switch mode {
		case guardToken:
			if !a.checkToken(r) {
				writeError(w, http.StatusUnauthorized, "unauthorized", "нужен X-Laya-Work-Token")
				return
			}
		case guardWrite:
			if origin := r.Header.Get("Origin"); origin != "" {
				if !sameOrigin(origin, r.Host) {
					writeError(w, http.StatusForbidden, "forbidden", "чужой Origin")
					return
				}
			} else if !a.checkToken(r) {
				writeError(w, http.StatusUnauthorized, "unauthorized", "нужен X-Laya-Work-Token или same-origin")
				return
			}
		}
		next(w, r)
	}
}

func (a *WorkAPI) checkToken(r *http.Request) bool {
	if a.token == "" {
		return false
	}
	got := r.Header.Get("X-Laya-Work-Token")
	if got == "" {
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			got = strings.TrimSpace(auth[7:])
		}
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(a.token)) == 1
}

func isLoopbackRemote(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, host) && (u.Scheme == "http" || u.Scheme == "https")
}

func (a *WorkAPI) handleWorkRedirect(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/work" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/work/", http.StatusTemporaryRedirect)
}

func (a *WorkAPI) handleWorkIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/work/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write(a.index)
}

func (a *WorkAPI) handleWorkAsset(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.URL.Path, "..") {
		http.NotFound(w, r)
		return
	}
	http.StripPrefix("/work/assets/", http.FileServer(http.FS(a.assetFS))).ServeHTTP(w, r)
}

// ---------------------------------------------------------------- API

func (a *WorkAPI) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method", "нужен GET")
		return
	}
	f, err := parseWorkFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_filter", err.Error())
		return
	}
	var st WorkStatus
	if a.status != nil {
		st = a.status()
	}
	ov, err := a.store.Overview(f, st)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store", "сводка недоступна")
		return
	}
	writeJSON(w, http.StatusOK, ov)
}

func (a *WorkAPI) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method", "нужен GET")
		return
	}
	f, err := parseWorkFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_filter", err.Error())
		return
	}
	list, err := a.store.List(f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store", "история недоступна")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *WorkAPI) handleEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method", "нужен GET")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/work/v1/events/")
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 120 || strings.ContainsAny(id, "/\\") {
		writeError(w, http.StatusBadRequest, "bad_id", "нужен event_id")
		return
	}
	ev, seq, related, err := a.store.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "событие не найдено")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"seq": seq, "event": ev, "related": related})
}

func (a *WorkAPI) handleLabels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method", "нужен POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req struct {
		EventID    string `json:"event_id"`
		CallID     string `json:"call_id"`
		QuestionID string `json:"question_id"`
		Expected   string `json:"expected"`
		Author     string `json:"author"`
		Comment    string `json:"comment"`
		Basis      string `json:"basis"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", "тело должно быть JSON")
		return
	}
	callID := req.CallID
	if callID == "" {
		callID = req.EventID
	}
	if callID == "" || strings.TrimSpace(req.Expected) == "" {
		writeError(w, http.StatusBadRequest, "bad_label", "нужны call_id и expected")
		return
	}
	label := WorkLabel{
		CallID: callID, QuestionID: req.QuestionID, Expected: strings.TrimSpace(req.Expected),
		Author: req.Author, Comment: req.Comment, Basis: req.Basis,
	}
	if err := a.store.Label(callID, req.QuestionID, label); err != nil {
		writeError(w, http.StatusBadRequest, "label_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "call_id": callID, "question_id": firstNonEmpty(req.QuestionID, "default")})
}

func (a *WorkAPI) handleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method", "нужен GET")
		return
	}
	f, err := parseWorkFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_filter", err.Error())
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "jsonl"
	}
	name := fmt.Sprintf("laya-work-%s.%s", time.Now().UTC().Format("20060102-150405"), format)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	}
	if err := a.store.Export(f, format, w); err != nil {
		// Заголовки уже отправлены — остаётся только оборвать ответ.
		return
	}
}

func (a *WorkAPI) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method", "нужен POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", "тело не прочитано")
		return
	}
	var events []WorkEvent
	var batch struct {
		Events []WorkEvent `json:"events"`
	}
	if json.Unmarshal(raw, &batch) == nil && len(batch.Events) > 0 {
		events = batch.Events
	} else {
		var one WorkEvent
		if json.Unmarshal(raw, &one) != nil {
			writeError(w, http.StatusBadRequest, "bad_json", "нужно событие или {events:[...]}")
			return
		}
		events = []WorkEvent{one}
	}
	if len(events) == 0 {
		writeError(w, http.StatusBadRequest, "empty", "events пуст")
		return
	}
	if len(events) > 100 {
		events = events[:100]
	}
	accepted := 0
	for i := range events {
		ev := events[i]
		if ev.Source == "" {
			ev.Source = "plugin"
		}
		if !a.cfg.Demo {
			ev.Demo = false
		}
		if err := ev.Normalize(a.cfg.MaxInputChars); err != nil {
			continue
		}
		if a.store.Record(ev) {
			accepted++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"accepted": accepted, "events": len(events)})
}

func (a *WorkAPI) handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method", "нужен POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	res, err := a.store.ImportJSONL(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "import_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleStream — SSE с Last-Event-ID. WriteTimeout сервера отключается на одно
// соединение, иначе длинный поток оборвался бы через 120 с.
func (a *WorkAPI) handleStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method", "нужен GET")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "stream", "SSE не поддерживается")
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	last := a.store.LastSeq()
	if h := r.Header.Get("Last-Event-ID"); h != "" {
		if n, err := parseInt64(h); err == nil && n > 0 {
			last = n
		}
	} else if s := r.URL.Query().Get("since"); s != "" {
		if n, err := parseInt64(s); err == nil && n > 0 {
			last = n
		}
	}
	if _, err := io.WriteString(w, "retry: 2000\n\n"); err != nil {
		return
	}
	flusher.Flush()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-tick.C:
			events, err := a.store.StreamAfter(last, 100)
			if err != nil {
				continue
			}
			for _, it := range events {
				payload, _ := json.Marshal(it.Event)
				if _, err := fmt.Fprintf(w, "id: %d\nevent: work\ndata: %s\n\n", it.Seq, payload); err != nil {
					return
				}
				last = it.Seq
			}
			if len(events) > 0 {
				flusher.Flush()
			}
		}
	}
}

func parseWorkFilter(r *http.Request) (WorkFilter, error) {
	q := r.URL.Query()
	f := WorkFilter{
		Session: q.Get("session"), Operation: q.Get("operation"), Status: q.Get("status"),
		Checkpoint: q.Get("checkpoint"),
	}
	if v := q.Get("from"); v != "" {
		n, err := parseInt64(v)
		if err != nil {
			return f, fmt.Errorf("from: %w", err)
		}
		f.From = n
	}
	if v := q.Get("to"); v != "" {
		n, err := parseInt64(v)
		if err != nil {
			return f, fmt.Errorf("to: %w", err)
		}
		f.To = n
	}
	if v := q.Get("range"); v != "" && f.From == 0 {
		now := time.Now().UnixMilli()
		switch v {
		case "1h":
			f.From = now - 3600_000
		case "6h":
			f.From = now - 6*3600_000
		case "24h":
			f.From = now - 24*3600_000
		case "7d":
			f.From = now - 7*24*3600_000
		case "30d":
			f.From = now - 30*24*3600_000
		case "all":
			f.From = 1 // не 0: 0 означает «не задано» и заменяется сутками
		}
		if f.To == 0 {
			f.To = now
		}
	}
	if f.From == 0 && f.To == 0 {
		f.To = time.Now().UnixMilli()
		f.From = f.To - 24*3600_000
	}
	parseBool := func(key string) *bool {
		v := q.Get(key)
		if v == "" {
			return nil
		}
		b := v == "1" || v == "true" || v == "yes"
		return &b
	}
	f.Error = parseBool("error")
	f.Fallback = parseBool("fallback")
	f.Truncated = parseBool("truncated")
	f.Labeled = parseBool("labeled")
	if v := q.Get("demo"); v != "" {
		b := v == "1" || v == "true" || v == "yes"
		f.Demo = &b
	}
	if v := q.Get("limit"); v != "" {
		if n, err := parseInt64(v); err == nil {
			f.Limit = int(n)
		}
	}
	if v := q.Get("before_seq"); v != "" {
		if n, err := parseInt64(v); err == nil {
			f.BeforeSeq = n
		}
	}
	return f, nil
}

func parseInt64(v string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &n)
	return n, err
}

// loadOrCreateToken читает токен из файла или создаёт его. Токен не пишется в
// лог; файл получает ACL, запрещающий наследование прав.
func loadOrCreateToken(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("путь токена пуст")
	}
	if b, err := os.ReadFile(path); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s, nil
		}
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw[:])
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	protectTokenFile(path)
	return token, nil
}

// protectTokenFile снимает наследование ACL. На Windows icacls вызывается без
// токена в аргументах; на остальных платформах достаточно chmod 0600.
func protectTokenFile(path string) {
	if runtime.GOOS != "windows" {
		_ = os.Chmod(path, 0o600)
		return
	}
	// SID вместо локализованных имён: на русской Windows «BUILTIN\Users» не
	// разбирается icacls. Права: SYSTEM/Administrators — полные, Users — чтение
	// токена (плагин работает от интерактивного пользователя).
	cmd := exec.Command("icacls", path, "/inheritance:r",
		"/grant:r", "*S-1-5-18:(F)",
		"/grant:r", "*S-1-5-32-544:(F)",
		"/grant:r", "*S-1-5-32-545:(R)")
	_ = cmd.Run()
}
