// Package server — хранилище событий Laya Work.
//
// Запись телеметрии не должна задерживать инференс: событие кладётся в
// ограниченную очередь и пишется отдельной горутиной батчами. При переполнении
// очередь считается потерянной (dropped), а не блокирует /decide. Ошибка БД
// логируется и не влияет на решение модели.
package server

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// WorkStoreConfig — параметры хранилища.
type WorkStoreConfig struct {
	DBPath        string
	KeepDays      int
	MaxEvents     int
	MaxBytes      int64
	MaxInputChars int
	QueueSize     int
	SpoolDir      string
}

// WorkStore — SQLite + очередь записи.
type WorkStore struct {
	db          *sql.DB
	cfg         WorkStoreConfig
	ch          chan WorkEvent
	barrier     chan chan struct{}
	done        chan struct{}
	wg          sync.WaitGroup
	closed      atomic.Bool
	dropped     atomic.Int64
	writeErrors atomic.Int64
	written     atomic.Int64
	spoolErrors atomic.Int64
	spoolDir    string
}

type workRow struct {
	Seq   int64
	Event WorkEvent
}

const workSchemaSQL = `
CREATE TABLE IF NOT EXISTS events (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id TEXT NOT NULL UNIQUE,
  trace_id TEXT NOT NULL DEFAULT '',
  call_id TEXT NOT NULL DEFAULT '',
  batch_id TEXT NOT NULL DEFAULT '',
  item_id TEXT NOT NULL DEFAULT '',
  session_id TEXT NOT NULL DEFAULT '',
  ts INTEGER NOT NULL,
  source TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL DEFAULT '',
  operation TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT '',
  checkpoint TEXT NOT NULL DEFAULT '',
  input_original TEXT NOT NULL DEFAULT '',
  input_effective TEXT NOT NULL DEFAULT '',
  input_chars INTEGER NOT NULL DEFAULT 0,
  original_chars INTEGER NOT NULL DEFAULT 0,
  effective_chars INTEGER NOT NULL DEFAULT 0,
  received_tokens INTEGER NOT NULL DEFAULT 0,
  processed_tokens INTEGER NOT NULL DEFAULT 0,
  dropped_tokens INTEGER NOT NULL DEFAULT 0,
  truncated INTEGER NOT NULL DEFAULT 0,
  selected TEXT NOT NULL DEFAULT '',
  policy_action TEXT NOT NULL DEFAULT '',
  laya_called INTEGER NOT NULL DEFAULT 0,
  fallback INTEGER NOT NULL DEFAULT 0,
  inference_ms REAL NOT NULL DEFAULT 0,
  total_ms REAL NOT NULL DEFAULT 0,
  labeled INTEGER NOT NULL DEFAULT 0,
  demo INTEGER NOT NULL DEFAULT 0,
  schema_version INTEGER NOT NULL DEFAULT 1,
  policy_version TEXT NOT NULL DEFAULT '',
  model_attempted INTEGER NOT NULL DEFAULT 0,
  question_json TEXT NOT NULL DEFAULT '',
  model_result_json TEXT NOT NULL DEFAULT '',
  policy_json TEXT NOT NULL DEFAULT '',
  timing_json TEXT NOT NULL DEFAULT '',
  evidence_json TEXT NOT NULL DEFAULT '',
  event_json TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_ts ON events(ts);
CREATE INDEX IF NOT EXISTS idx_events_session ON events(session_id);
CREATE INDEX IF NOT EXISTS idx_events_operation ON events(operation);
CREATE INDEX IF NOT EXISTS idx_events_status ON events(status);
CREATE INDEX IF NOT EXISTS idx_events_trace ON events(trace_id);
CREATE INDEX IF NOT EXISTS idx_events_call ON events(call_id);
CREATE INDEX IF NOT EXISTS idx_events_checkpoint ON events(checkpoint);
CREATE INDEX IF NOT EXISTS idx_events_kind ON events(kind);
CREATE TABLE IF NOT EXISTS labels (
  call_id TEXT NOT NULL,
  question_id TEXT NOT NULL,
  expected TEXT NOT NULL,
  author TEXT NOT NULL DEFAULT '',
  comment TEXT NOT NULL DEFAULT '',
  basis TEXT NOT NULL DEFAULT '',
  ts INTEGER NOT NULL,
  PRIMARY KEY (call_id, question_id)
);
CREATE TABLE IF NOT EXISTS meta (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
INSERT OR REPLACE INTO meta(key, value) VALUES ('schema_version', '1');
INSERT OR REPLACE INTO meta(key, value) VALUES ('policy_version', 'work-v1');
PRAGMA user_version = 2;
`

// OpenWorkStore открывает/создаёт БД и запускает writer.
func OpenWorkStore(cfg WorkStoreConfig) (*WorkStore, error) {
	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join("data", "work.db")
	}
	if cfg.KeepDays <= 0 {
		cfg.KeepDays = 30
	}
	if cfg.MaxEvents <= 0 {
		cfg.MaxEvents = 50_000
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = 512 << 20
	}
	if cfg.MaxInputChars <= 0 {
		cfg.MaxInputChars = 64 << 10
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 1024
	}
	if dir := filepath.Dir(cfg.DBPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("work: каталог БД: %w", err)
		}
	}
	dsn := "file:" + filepath.ToSlash(cfg.DBPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("work: открыть БД: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000; PRAGMA journal_mode = WAL; PRAGMA synchronous = NORMAL;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("work: pragmas: %w", err)
	}
	if err := migrateWorkDB(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("work: миграция: %w", err)
	}
	if _, err := db.Exec(workSchemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("work: схема: %w", err)
	}
	if err := ensureWorkColumns(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("work: колонки: %w", err)
	}
	s := &WorkStore{
		db:      db,
		cfg:     cfg,
		ch:      make(chan WorkEvent, cfg.QueueSize),
		barrier: make(chan chan struct{}),
		done:    make(chan struct{}),
	}
	s.wg.Add(1)
	go s.writer()
	_ = s.Prune()
	return s, nil
}

// migrateWorkDB снимает несовместимую старую таблицу меток (схема до
// call_id/question_id). События не удаляются: колонки добавляются отдельно.
func migrateWorkDB(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(labels)`)
	if err != nil {
		return nil
	}
	hasEventID, hasCallID := false, false
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt any
		if rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk) == nil {
			if name == "event_id" {
				hasEventID = true
			}
			if name == "call_id" {
				hasCallID = true
			}
		}
	}
	_ = rows.Close()
	if hasEventID && !hasCallID {
		if _, err := db.Exec(`DROP TABLE labels`); err != nil {
			return err
		}
	}
	return nil
}

func ensureWorkColumns(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(events)`)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt any
		if rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk) == nil {
			have[name] = true
		}
	}
	_ = rows.Close()
	add := map[string]string{
		"original_chars":   "INTEGER NOT NULL DEFAULT 0",
		"effective_chars":  "INTEGER NOT NULL DEFAULT 0",
		"received_tokens":  "INTEGER NOT NULL DEFAULT 0",
		"processed_tokens": "INTEGER NOT NULL DEFAULT 0",
		"dropped_tokens":   "INTEGER NOT NULL DEFAULT 0",
		"schema_version":   "INTEGER NOT NULL DEFAULT 1",
		"policy_version":   "TEXT NOT NULL DEFAULT ''",
		"model_attempted":  "INTEGER NOT NULL DEFAULT 0",
	}
	for col, ddl := range add {
		if have[col] {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE events ADD COLUMN ` + col + ` ` + ddl); err != nil {
			return err
		}
	}
	return nil
}

// Record кладёт событие в очередь. false — очередь переполнена/закрыта.
func (s *WorkStore) Record(ev WorkEvent) bool {
	if s == nil || s.closed.Load() {
		return false
	}
	select {
	case s.ch <- ev:
		return true
	default:
		s.dropped.Add(1)
		return false
	}
}

// Flush ждёт, пока writer запишет всё, что уже в очереди.
func (s *WorkStore) Flush(timeout time.Duration) bool {
	if s == nil || s.closed.Load() {
		return false
	}
	done := make(chan struct{})
	select {
	case s.barrier <- done:
	case <-time.After(timeout):
		return false
	}
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// Stats — состояние очереди и счётчики.
func (s *WorkStore) Stats() WorkStoreStats {
	if s == nil {
		return WorkStoreStats{}
	}
	return WorkStoreStats{
		Enabled:     !s.closed.Load(),
		Dropped:     s.dropped.Load(),
		WriteErrors: s.writeErrors.Load(),
		Written:     s.written.Load(),
		SpoolErrors: s.spoolErrors.Load(),
		Queue:       len(s.ch),
		DBPath:      s.cfg.DBPath,
		SpoolDir:    s.spoolDir,
	}
}

// Close останавливает writer и закрывает БД.
func (s *WorkStore) Close() error {
	if s == nil {
		return nil
	}
	if s.closed.Swap(true) {
		return nil
	}
	close(s.done)
	s.wg.Wait()
	return s.db.Close()
}

func (s *WorkStore) writer() {
	defer s.wg.Done()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	batch := make([]WorkEvent, 0, 64)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		inserted, _ := s.insertBatch(batch)
		s.written.Add(int64(inserted))
		batch = batch[:0]
	}
	for {
		select {
		case ev := <-s.ch:
			batch = append(batch, ev)
			if len(batch) >= 64 {
				flush()
			}
		case b := <-s.barrier:
		drain:
			for {
				select {
				case ev := <-s.ch:
					batch = append(batch, ev)
				default:
					flush()
					close(b)
					break drain
				}
			}
		case <-ticker.C:
			flush()
		case <-s.done:
			for {
				select {
				case ev := <-s.ch:
					batch = append(batch, ev)
				default:
					flush()
					return
				}
			}
		}
	}
}

func (s *WorkStore) insertBatch(events []WorkEvent) (int, int) {
	if len(events) == 0 {
		return 0, 0
	}
	tx, err := s.db.Begin()
	if err != nil {
		s.writeErrors.Add(1)
		log.Printf("[LAYA] work: begin: %v", err)
		return 0, 0
	}
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO events (
		event_id, trace_id, call_id, batch_id, item_id, session_id, ts, source, kind,
		operation, status, checkpoint, input_original, input_effective, input_chars,
		original_chars, effective_chars, received_tokens, processed_tokens, dropped_tokens,
		truncated, selected, policy_action, laya_called, fallback, inference_ms,
		total_ms, labeled, demo, schema_version, policy_version, model_attempted,
		question_json, model_result_json, policy_json, timing_json, evidence_json, event_json
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		s.writeErrors.Add(1)
		log.Printf("[LAYA] work: prepare: %v", err)
		return 0, 0
	}
	defer stmt.Close()
	inserted, duplicates := 0, 0
	for _, ev := range events {
		raw, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		idx := ev.Index()
		ts := workEventMillis(ev.Timestamp)
		qjson, _ := json.Marshal(ev.Questions)
		res, err := stmt.Exec(
			ev.EventID, ev.TraceID, ev.CallID, ev.BatchID, ev.ItemID, ev.SessionID,
			ts, ev.Source, ev.Kind, ev.Operation, ev.Status, ev.Model.Checkpoint,
			ev.Input.OriginalRedacted, ev.Input.EffectiveRedacted, ev.Input.Chars,
			ev.Input.OriginalChars, ev.Input.EffectiveChars, ev.Input.ReceivedTokens,
			ev.Input.ProcessedTokens, ev.Input.DroppedTokens,
			boolInt(ev.Truncated || ev.Input.Truncated), idx.Selected, idx.PolicyAction,
			boolInt(idx.LayaCalled), boolInt(idx.Fallback), idx.InferenceMS, idx.TotalMS,
			0, boolInt(ev.Demo), ev.SchemaVersion, ev.PolicyVersion, boolInt(ev.ModelAttempted),
			string(qjson), string(ev.ModelResult), string(ev.PolicyResult), mustJSON(ev.Timing),
			string(ev.Evidence), string(raw),
		)
		if err != nil {
			duplicates++
			continue
		}
		n, _ := res.RowsAffected()
		if n > 0 {
			inserted++
		} else {
			duplicates++
		}
	}
	if err := tx.Commit(); err != nil {
		s.writeErrors.Add(1)
		log.Printf("[LAYA] work: commit: %v", err)
		return 0, 0
	}
	return inserted, duplicates
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func workEventMillis(ts string) int64 {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t.UnixMilli()
		}
	}
	return time.Now().UnixMilli()
}

// Prune удаляет старые события и лишние строки сверх лимита.
func (s *WorkStore) Prune() error {
	if s == nil || s.db == nil {
		return nil
	}
	cutoff := time.Now().Add(-time.Duration(s.cfg.KeepDays) * 24 * time.Hour).UnixMilli()
	if _, err := s.db.Exec(`DELETE FROM events WHERE ts < ?`, cutoff); err != nil {
		return err
	}
	if s.cfg.MaxEvents > 0 {
		_, _ = s.db.Exec(`DELETE FROM events WHERE seq NOT IN (SELECT seq FROM events ORDER BY seq DESC LIMIT ?)`, s.cfg.MaxEvents)
	}
	// Лимит объёма: удаляем самые старые записи порциями, пока файл больше
	// настройки. Ограничение по байтам защищает диск от роста при интенсивной
	// телеметрии.
	for i := 0; i < 1000; i++ {
		var pageCount, pageSize int64
		if err := s.db.QueryRow(`PRAGMA page_count`).Scan(&pageCount); err != nil {
			break
		}
		if err := s.db.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
			break
		}
		if pageCount*pageSize <= s.cfg.MaxBytes {
			break
		}
		res, err := s.db.Exec(`DELETE FROM events WHERE seq IN (SELECT seq FROM events ORDER BY seq ASC LIMIT 1000)`)
		if err != nil {
			break
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			break
		}
	}
	_, _ = s.db.Exec(`DELETE FROM labels WHERE call_id NOT IN (SELECT call_id FROM events WHERE call_id <> '') AND call_id NOT IN (SELECT event_id FROM events)`)
	return nil
}

// Maintain запускает фоновую чистку; останавливается вместе с ctx или Close.
func (s *WorkStore) Maintain(ctx context.Context) {
	if s == nil {
		return
	}
	s.wg.Add(1)
	defer s.wg.Done()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case <-t.C:
			if err := s.Prune(); err != nil {
				log.Printf("[LAYA] work: prune: %v", err)
			}
		}
	}
}

// StartSpool запускает приём файлов от плагина. Это транспорт без сетевого
// ожидания: hook пишет маленький файл и сразу завершается, а сервер забирает
// его при следующем тике. Если сервер лежит, файлы ждут на диске и будут
// прочитаны после старта.
func (s *WorkStore) StartSpool(ctx context.Context, dir string) {
	if s == nil || dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o777); err != nil {
		log.Printf("[LAYA] work: spool-каталог недоступен: %v", err)
		return
	}
	if runtime.GOOS == "windows" {
		// Плагин работает от пользователя, сервер может быть запущен как SYSTEM:
		// группа Users должна иметь право создавать файлы обмена.
		_ = exec.Command("icacls", dir, "/grant", "*S-1-5-32-545:(OI)(CI)M").Run()
	}
	s.spoolDir = dir
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.done:
				return
			case <-t.C:
				s.ingestSpool(dir)
			}
		}
	}()
}

func (s *WorkStore) ingestSpool(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") && !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		full := filepath.Join(dir, name)
		raw, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		events := decodeSpoolEvents(raw)
		if len(events) == 0 {
			if len(strings.TrimSpace(string(raw))) > 0 {
				s.spoolErrors.Add(1)
			}
			_ = os.Remove(full)
			continue
		}
		valid := make([]WorkEvent, 0, len(events))
		for _, ev := range events {
			if ev.Source == "" {
				ev.Source = "plugin"
			}
			if err := ev.Normalize(s.cfg.MaxInputChars); err != nil {
				s.spoolErrors.Add(1)
				continue
			}
			valid = append(valid, ev)
		}
		if len(valid) > 0 {
			inserted, duplicates := s.insertBatch(valid)
			s.written.Add(int64(inserted))
			if inserted == 0 && duplicates == 0 {
				// Запись не удалась: оставляем файл до следующего тика, но не
				// крутимся вечно на одном и том же битом файле.
				_ = os.Rename(full, full+".bad")
				s.spoolErrors.Add(1)
				continue
			}
		}
		_ = os.Remove(full)
	}
}

func decodeSpoolEvents(raw []byte) []WorkEvent {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil
	}
	var batch struct {
		Events []WorkEvent `json:"events"`
	}
	if json.Unmarshal(raw, &batch) == nil && len(batch.Events) > 0 {
		return batch.Events
	}
	var one WorkEvent
	if json.Unmarshal(raw, &one) == nil && one.EventID != "" {
		return []WorkEvent{one}
	}
	var out []WorkEvent
	sc := bufio.NewScanner(strings.NewReader(trimmed))
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev WorkEvent
		if json.Unmarshal([]byte(line), &ev) == nil && ev.EventID != "" {
			out = append(out, ev)
		}
	}
	return out
}

// DeleteDemo удаляет демонстрационные записи (нормальный режим не должен их
// показывать даже после перезапуска).
func (s *WorkStore) DeleteDemo() {
	if s == nil {
		return
	}
	_, _ = s.db.Exec(`DELETE FROM events WHERE demo = 1`)
	_, _ = s.db.Exec(`DELETE FROM labels WHERE call_id NOT IN (SELECT call_id FROM events WHERE call_id <> '') AND call_id NOT IN (SELECT event_id FROM events)`)
}

// ---------------------------------------------------------------- фильтры

// WorkFilter — фильтры ленты и сводки.
type WorkFilter struct {
	From       int64
	To         int64
	Session    string
	Operation  string
	Status     string
	Checkpoint string
	Error      *bool
	Fallback   *bool
	Truncated  *bool
	Labeled    *bool
	Demo       *bool
	Limit      int
	BeforeSeq  int64
}

func (f WorkFilter) normalized() WorkFilter {
	if f.To == 0 {
		f.To = time.Now().UnixMilli()
	}
	if f.From == 0 {
		f.From = f.To - 24*60*60*1000
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 200 {
		f.Limit = 200
	}
	return f
}

func (f WorkFilter) where() (string, []any) {
	return f.whereAlias("")
}

// whereAlias строит условия с префиксом таблицы. Нужен для JOIN labels/events,
// где call_id есть в обеих таблицах и без префикса SQL был бы неоднозначным.
func (f WorkFilter) whereAlias(alias string) (string, []any) {
	f = f.normalized()
	col := func(name string) string {
		if alias == "" {
			return name
		}
		return alias + "." + name
	}
	parts := []string{col("ts") + " >= ?", col("ts") + " <= ?"}
	args := []any{f.From, f.To}
	if f.Session != "" {
		parts = append(parts, col("session_id")+" = ?")
		args = append(args, f.Session)
	}
	if f.Operation != "" {
		parts = append(parts, col("operation")+" = ?")
		args = append(args, f.Operation)
	}
	if f.Status != "" {
		parts = append(parts, col("status")+" = ?")
		args = append(args, f.Status)
	}
	if f.Checkpoint != "" {
		parts = append(parts, col("checkpoint")+" = ?")
		args = append(args, f.Checkpoint)
	}
	if f.Error != nil && *f.Error {
		parts = append(parts, "("+col("status")+" = 'error' OR "+col("status")+" = 'timeout')")
	}
	if f.Fallback != nil && *f.Fallback {
		parts = append(parts, col("fallback")+" = 1")
	}
	if f.Truncated != nil && *f.Truncated {
		parts = append(parts, col("truncated")+" = 1")
	}
	if f.Labeled != nil {
		outer := alias
		if outer == "" {
			outer = "events"
		}
		exists := "EXISTS (SELECT 1 FROM labels l WHERE l.call_id = " + outer + ".call_id" +
			" OR l.call_id = " + outer + ".event_id)"
		if *f.Labeled {
			parts = append(parts, exists)
		} else {
			parts = append(parts, "NOT "+exists)
		}
	}
	demo := false
	if f.Demo != nil {
		demo = *f.Demo
	}
	parts = append(parts, col("demo")+" = ?")
	args = append(args, boolInt(demo))
	if f.BeforeSeq > 0 {
		parts = append(parts, col("seq")+" < ?")
		args = append(args, f.BeforeSeq)
	}
	return strings.Join(parts, " AND "), args
}

// WorkListEvent — строка ленты.
type WorkListEvent struct {
	Seq   int64     `json:"seq"`
	Event WorkEvent `json:"event"`
}

// WorkRelatedEvent — связанное событие с типом связи.
type WorkRelatedEvent struct {
	Seq      int64     `json:"seq"`
	Relation string    `json:"relation"`
	Event    WorkEvent `json:"event"`
}

// WorkList — страница истории.
type WorkList struct {
	Events        []WorkListEvent `json:"events"`
	NextBeforeSeq int64           `json:"next_before_seq,omitempty"`
	LastSeq       int64           `json:"last_seq"`
}

// List возвращает страницу событий (новые первыми).
func (s *WorkStore) List(f WorkFilter) (WorkList, error) {
	f = f.normalized()
	where, args := f.where()
	args = append(args, f.Limit)
	rows, err := s.db.Query(`SELECT seq, event_json FROM events WHERE `+where+` ORDER BY seq DESC LIMIT ?`, args...)
	if err != nil {
		return WorkList{}, err
	}
	defer rows.Close()
	out := WorkList{Events: []WorkListEvent{}}
	for rows.Next() {
		var seq int64
		var raw string
		if err := rows.Scan(&seq, &raw); err != nil {
			return out, err
		}
		var ev WorkEvent
		if json.Unmarshal([]byte(raw), &ev) == nil {
			out.Events = append(out.Events, WorkListEvent{Seq: seq, Event: ev})
		}
	}
	if len(out.Events) == f.Limit && len(out.Events) > 0 {
		out.NextBeforeSeq = out.Events[len(out.Events)-1].Seq
	}
	out.Events = mergeDecisionRows(out.Events)
	out.LastSeq = s.LastSeq()
	return out, rows.Err()
}

// mergeDecisionRows склеивает engine- и plugin-события одного call_id в одну
// строку ленты. Engine — источник истины по ответу модели, plugin — по политике;
// это одно решение, а не два. События без call_id и tool/agent-события остаются
// отдельными строками.
func mergeDecisionRows(events []WorkListEvent) []WorkListEvent {
	index := map[string]int{}
	out := make([]WorkListEvent, 0, len(events))
	for _, it := range events {
		e := it.Event
		if e.Kind != "model_call" || e.CallID == "" {
			out = append(out, it)
			continue
		}
		if pos, ok := index[e.CallID]; ok {
			existing := out[pos].Event
			if existing.Source != "engine" && e.Source == "engine" {
				if len(e.PolicyResult) == 0 {
					e.PolicyResult = existing.PolicyResult
				}
				if len(e.Labels) == 0 {
					e.Labels = existing.Labels
				}
				out[pos].Event = e
			}
			continue
		}
		index[e.CallID] = len(out)
		out = append(out, it)
	}
	// Политика с тем же call_id дополняет строку решения, а не создаёт вторую.
	result := make([]WorkListEvent, 0, len(out))
	for _, it := range out {
		e := it.Event
		if e.Kind == "policy" && e.CallID != "" {
			if pos, ok := index[e.CallID]; ok {
				if len(out[pos].Event.PolicyResult) == 0 {
					out[pos].Event.PolicyResult = e.PolicyResult
				}
				continue
			}
		}
		result = append(result, it)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Seq > result[j].Seq })
	return result
}

// StreamAfter возвращает события после seq для SSE.
func (s *WorkStore) StreamAfter(seq int64, limit int) ([]WorkListEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT seq, event_json FROM events WHERE seq > ? ORDER BY seq ASC LIMIT ?`, seq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WorkListEvent{}
	for rows.Next() {
		var n int64
		var raw string
		if rows.Scan(&n, &raw) != nil {
			continue
		}
		var ev WorkEvent
		if json.Unmarshal([]byte(raw), &ev) == nil {
			out = append(out, WorkListEvent{Seq: n, Event: ev})
		}
	}
	return out, rows.Err()
}

// LastSeq — максимальный seq в БД (для SSE).
func (s *WorkStore) LastSeq() int64 {
	var seq int64
	_ = s.db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&seq)
	return seq
}

// Get возвращает событие, его метки по вопросам и связанные события по
// trace_id, а также действия агента из той же сессии в окне ±2 минуты.
func (s *WorkStore) Get(eventID string) (WorkEvent, int64, []WorkRelatedEvent, error) {
	var seq int64
	var raw string
	err := s.db.QueryRow(`SELECT seq, event_json FROM events WHERE event_id = ?`, eventID).
		Scan(&seq, &raw)
	if err != nil {
		return WorkEvent{}, 0, nil, err
	}
	var ev WorkEvent
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		return WorkEvent{}, 0, nil, err
	}
	s.attachLabels(&ev)
	related := []WorkRelatedEvent{}
	seen := map[int64]bool{seq: true}
	if ev.TraceID != "" {
		rows, err := s.db.Query(`SELECT seq, event_json FROM events WHERE trace_id = ? AND event_id <> ? ORDER BY seq LIMIT 50`, ev.TraceID, ev.EventID)
		if err == nil {
			for rows.Next() {
				var rseq int64
				var rraw string
				if rows.Scan(&rseq, &rraw) == nil {
					var rev WorkEvent
					if json.Unmarshal([]byte(rraw), &rev) == nil && !seen[rseq] {
						seen[rseq] = true
						s.attachLabels(&rev)
						related = append(related, WorkRelatedEvent{Seq: rseq, Relation: "trace", Event: rev})
					}
				}
			}
			rows.Close()
		}
	}
	if ev.SessionID != "" {
		ts := workEventMillis(ev.Timestamp)
		rows, err := s.db.Query(`SELECT seq, event_json FROM events
			WHERE session_id = ? AND kind IN ('agent_action','check') AND ts BETWEEN ? AND ?
			ORDER BY seq DESC LIMIT 20`, ev.SessionID, ts-120_000, ts+120_000)
		if err == nil {
			for rows.Next() {
				var rseq int64
				var rraw string
				if rows.Scan(&rseq, &rraw) == nil {
					var rev WorkEvent
					if json.Unmarshal([]byte(rraw), &rev) == nil && !seen[rseq] {
						seen[rseq] = true
						s.attachLabels(&rev)
						related = append(related, WorkRelatedEvent{Seq: rseq, Relation: "session", Event: rev})
					}
				}
			}
			rows.Close()
		}
	}
	// В карточке решения показываем политику плагина, даже если открыт
	// engine-ивент: это одно решение, связанное общим call_id/trace_id.
	if len(ev.PolicyResult) == 0 {
		for _, r := range related {
			if len(r.Event.PolicyResult) == 0 {
				continue
			}
			if r.Event.CallID == ev.CallID || r.Event.TraceID == ev.TraceID {
				ev.PolicyResult = r.Event.PolicyResult
				break
			}
		}
	}
	return ev, seq, related, nil
}

// attachLabels подмешивает метки вопросов к событию. Метка привязана к
// call_id/question_id; для вызовов без call_id ключом служит event_id.
func (s *WorkStore) attachLabels(ev *WorkEvent) {
	key := ev.CallID
	if key == "" {
		key = ev.EventID
	}
	rows, err := s.db.Query(`SELECT question_id, expected, author, comment, basis, ts
		FROM labels WHERE call_id = ? ORDER BY question_id`, key)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var qid, expected, author, comment, basis string
		var ts int64
		if rows.Scan(&qid, &expected, &author, &comment, &basis, &ts) != nil {
			continue
		}
		ev.Labels = append(ev.Labels, WorkLabel{
			CallID: key, QuestionID: qid, Expected: expected, Author: author,
			Comment: comment, Basis: basis,
			TS: time.UnixMilli(ts).UTC().Format(time.RFC3339Nano),
		})
	}
}

// Label сохраняет ручную разметку конкретного вопроса вызова.
func (s *WorkStore) Label(callID, questionID string, label WorkLabel) error {
	if strings.TrimSpace(label.Expected) == "" {
		return fmt.Errorf("expected пуст")
	}
	if callID == "" {
		return fmt.Errorf("call_id пуст")
	}
	if questionID == "" {
		questionID = "default"
	}
	var one int
	if err := s.db.QueryRow(`SELECT 1 FROM events WHERE call_id = ? OR event_id = ? LIMIT 1`, callID, callID).Scan(&one); err != nil {
		return fmt.Errorf("событие не найдено: %s", callID)
	}
	if label.TS == "" {
		label.TS = time.Now().UTC().Format(time.RFC3339Nano)
	}
	label.CallID = callID
	label.QuestionID = questionID
	ts := workEventMillis(label.TS)
	if _, err := s.db.Exec(`INSERT INTO labels(call_id, question_id, expected, author, comment, basis, ts)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(call_id, question_id) DO UPDATE SET expected=excluded.expected,
		author=excluded.author, comment=excluded.comment, basis=excluded.basis, ts=excluded.ts`,
		callID, questionID, label.Expected, label.Author, label.Comment, label.Basis, ts); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE events SET labeled = 1 WHERE call_id = ? OR event_id = ?`, callID, callID)
	return err
}

// Export пишет отфильтрованные события в JSONL или CSV.
func (s *WorkStore) Export(f WorkFilter, format string, w io.Writer) error {
	f = f.normalized()
	where, args := f.where()
	rows, err := s.db.Query(`SELECT seq, event_json, event_id, ts, session_id, operation, status,
		selected, policy_action, inference_ms, total_ms FROM events WHERE `+where+` ORDER BY seq DESC`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	if format == "csv" {
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"seq", "event_id", "ts", "session_id", "operation", "status", "selected", "policy_action", "inference_ms", "total_ms"})
		for rows.Next() {
			var seq, ts int64
			var raw, eventID, session, operation, status, selected, policy string
			var inf, total float64
			if err := rows.Scan(&seq, &raw, &eventID, &ts, &session, &operation, &status, &selected, &policy, &inf, &total); err != nil {
				return err
			}
			_ = raw
			_ = cw.Write([]string{
				strconv.FormatInt(seq, 10), eventID, time.UnixMilli(ts).UTC().Format(time.RFC3339),
				session, operation, status, selected, policy,
				strconv.FormatFloat(inf, 'f', 3, 64), strconv.FormatFloat(total, 'f', 3, 64),
			})
		}
		cw.Flush()
		return cw.Error()
	}
	for rows.Next() {
		var seq, ts int64
		var raw string
		var rest [8]any
		_ = rest
		var eventID, session, operation, status, selected, policy string
		var inf, total float64
		if err := rows.Scan(&seq, &raw, &eventID, &ts, &session, &operation, &status, &selected, &policy, &inf, &total); err != nil {
			return err
		}
		if _, err := io.WriteString(w, raw+"\n"); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ---------------------------------------------------------------- агрегаты

// WorkTotals — сводные счётчики.
type WorkTotals struct {
	Events         int64 `json:"events"`
	InferenceCalls int64 `json:"inference_calls"`
	RuleOnly       int64 `json:"rule_only"`
	Errors         int64 `json:"errors"`
	ClientErrors   int64 `json:"client_errors"`
	Timeouts       int64 `json:"timeouts"`
	Fallbacks      int64 `json:"fallbacks"`
	Truncated      int64 `json:"truncated"`
	Labeled        int64 `json:"labeled"`
	Unlabeled      int64 `json:"unlabeled"`
	Demo           int64 `json:"demo"`
}

// WorkLatency — p50/p95 двух шкал.
type WorkLatency struct {
	Inference WorkPercentiles `json:"inference"`
	Total     WorkPercentiles `json:"total"`
}

// WorkPercentiles — пара квантилей.
type WorkPercentiles struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
}

// WorkBucket — точка timeline.
type WorkBucket struct {
	T      int64 `json:"t"`
	Model  int64 `json:"model"`
	Policy int64 `json:"policy"`
	Error  int64 `json:"error"`
}

// WorkCount — простая пара «значение/количество».
type WorkCount struct {
	Label string `json:"label"`
	Count int64  `json:"count"`
}

// WorkClassMetric — precision/recall по классу.
type WorkClassMetric struct {
	Label     string  `json:"label"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	Support   int64   `json:"support"`
}

// WorkAccuracy — точность только по размеченным примерам.
type WorkAccuracy struct {
	Labeled      int64             `json:"labeled"`
	LabeledCalls int64             `json:"labeled_calls"`
	Unlabeled    int64             `json:"unlabeled"`
	Matches      int64             `json:"matches"`
	Accuracy     float64           `json:"accuracy"`
	MAE          *float64          `json:"mae,omitempty"`
	Labels       []string          `json:"labels"`
	Confusion    [][]int64         `json:"confusion"`
	PerClass     []WorkClassMetric `json:"per_class"`
}

// WorkSavings — сокращение выдачи инструмента (не экономия сессии/стоимости).
type WorkSavings struct {
	Tool          string `json:"tool"`
	Events        int64  `json:"events"`
	SourceBytes   int64  `json:"source_bytes"`
	ReturnedBytes int64  `json:"returned_bytes"`
	FollowupBytes int64  `json:"followup_bytes"`
}

// WorkRange — фактический период.
type WorkRange struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}

// WorkOverview — ответ /api/work/v1/overview.
type WorkOverview struct {
	Server        WorkStatus     `json:"server"`
	Work          WorkStoreStats `json:"work"`
	SchemaVersion int            `json:"schema_version"`
	PolicyVersion string         `json:"policy_version"`
	Range         WorkRange      `json:"range"`
	Totals        WorkTotals     `json:"totals"`
	Latency       WorkLatency    `json:"latency"`
	Timeline      []WorkBucket   `json:"timeline"`
	ByOperation   []WorkCount    `json:"by_operation"`
	ByStatus      []WorkCount    `json:"by_status"`
	Selected      []WorkCount    `json:"selected"`
	Accuracy      WorkAccuracy   `json:"accuracy"`
	Savings       []WorkSavings  `json:"savings"`
}

// Overview считает сводку и графики.
func (s *WorkStore) Overview(f WorkFilter, status WorkStatus) (WorkOverview, error) {
	f = f.normalized()
	where, args := f.where()
	out := WorkOverview{
		Server: status, Work: s.Stats(), SchemaVersion: WorkSchemaVersion, PolicyVersion: WorkPolicyVersion,
		Range:    WorkRange{From: f.From, To: f.To},
		Timeline: []WorkBucket{}, ByOperation: []WorkCount{}, ByStatus: []WorkCount{},
		Selected: []WorkCount{}, Savings: []WorkSavings{},
	}
	row := s.db.QueryRow(`SELECT COUNT(*),
		COUNT(DISTINCT CASE WHEN kind='model_call' AND source='engine' AND model_attempted=1 THEN COALESCE(NULLIF(call_id,''), event_id) END),
		COUNT(DISTINCT CASE WHEN kind='policy' AND laya_called=0 THEN COALESCE(NULLIF(call_id,''), event_id) END),
		COALESCE(SUM(CASE WHEN status='error' AND (model_attempted=1 OR source <> 'engine') THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='error' AND source='engine' AND model_attempted=0 THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='timeout' AND (model_attempted=1 OR source <> 'engine') THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN fallback=1 OR status='fallback' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN truncated=1 THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN labeled=1 THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN demo=1 THEN 1 ELSE 0 END),0)
		FROM events WHERE `+where, args...)
	if err := row.Scan(&out.Totals.Events, &out.Totals.InferenceCalls, &out.Totals.RuleOnly,
		&out.Totals.Errors, &out.Totals.ClientErrors, &out.Totals.Timeouts, &out.Totals.Fallbacks,
		&out.Totals.Truncated, &out.Totals.Labeled, &out.Totals.Demo); err != nil {
		return out, err
	}

	var inf, total []float64
	if rows, err := s.db.Query(`SELECT inference_ms FROM events WHERE `+where+` AND kind='model_call' AND source='engine' AND model_attempted=1 AND inference_ms > 0`, args...); err == nil {
		for rows.Next() {
			var v float64
			if rows.Scan(&v) == nil {
				inf = append(inf, v)
			}
		}
		rows.Close()
	}
	if rows, err := s.db.Query(`SELECT total_ms FROM events WHERE `+where+` AND total_ms > 0`, args...); err == nil {
		for rows.Next() {
			var v float64
			if rows.Scan(&v) == nil {
				total = append(total, v)
			}
		}
		rows.Close()
	}
	sort.Float64s(inf)
	sort.Float64s(total)
	out.Latency.Inference = WorkPercentiles{P50: percentile(inf, 0.5), P95: percentile(inf, 0.95)}
	out.Latency.Total = WorkPercentiles{P50: percentile(total, 0.5), P95: percentile(total, 0.95)}

	bucket := int64(60_000)
	if f.To-f.From > 48*60*60*1000 {
		bucket = 24 * 60 * 60 * 1000
	} else if f.To-f.From > 6*60*60*1000 {
		bucket = 60 * 60 * 1000
	}
	buckets := map[int64]WorkBucket{}
	if rows, err := s.db.Query(`SELECT (ts / ?) * ? AS b,
		COALESCE(SUM(CASE WHEN kind='model_call' AND source='engine' AND model_attempted=1 THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN kind='policy' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status IN ('error','timeout') THEN 1 ELSE 0 END),0)
		FROM events WHERE `+where+` GROUP BY b ORDER BY b`, append([]any{bucket, bucket}, args...)...); err == nil {
		for rows.Next() {
			var b WorkBucket
			if rows.Scan(&b.T, &b.Model, &b.Policy, &b.Error) == nil {
				buckets[b.T] = b
			}
		}
		rows.Close()
	}
	// Непрерывная шкала: пустые интервалы тоже видны, иначе один столбец
	// растягивается на весь график и врёт о форме нагрузки.
	timelineFrom := f.From
	if f.To-timelineFrom > 60*24*60*60*1000 {
		timelineFrom = f.To - 60*24*60*60*1000
	}
	start := (timelineFrom / bucket) * bucket
	for t := start; t <= f.To && len(out.Timeline) < 2000; t += bucket {
		if b, ok := buckets[t]; ok {
			out.Timeline = append(out.Timeline, b)
		} else {
			out.Timeline = append(out.Timeline, WorkBucket{T: t})
		}
	}

	out.ByOperation = s.countBy(f, "operation", 20)
	out.ByStatus = s.countBy(f, "status", 20)
	out.Selected = s.countBy(f, "selected", 20)
	out.Accuracy = s.accuracy(f)
	out.Totals.Labeled = out.Accuracy.Labeled
	if out.Totals.InferenceCalls > out.Accuracy.LabeledCalls {
		out.Totals.Unlabeled = out.Totals.InferenceCalls - out.Accuracy.LabeledCalls
	}
	out.Savings = s.savings(f)
	return out, nil
}

func (s *WorkStore) countBy(f WorkFilter, column string, limit int) []WorkCount {
	where, args := f.where()
	// column — только из фиксированного списка вызывающих.
	rows, err := s.db.Query(`SELECT `+column+`, COUNT(*) FROM events WHERE `+where+` AND `+column+` <> '' GROUP BY `+column+` ORDER BY COUNT(*) DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return []WorkCount{}
	}
	defer rows.Close()
	out := []WorkCount{}
	for rows.Next() {
		var c WorkCount
		if rows.Scan(&c.Label, &c.Count) == nil {
			out = append(out, c)
		}
	}
	return out
}

func (s *WorkStore) accuracy(f WorkFilter) WorkAccuracy {
	where, args := f.whereAlias("e")
	out := WorkAccuracy{Labels: []string{}, Confusion: [][]int64{}, PerClass: []WorkClassMetric{}}
	// Один вопрос может быть описан engine- и plugin-событием. Предпочитаем
	// engine (source='engine'), чтобы не считать одно решение дважды и брать
	// ответ из фактического обмена с моделью.
	rows, err := s.db.Query(`SELECT e.event_json, e.source, l.call_id, l.question_id, l.expected
		FROM labels l JOIN events e ON (l.call_id = e.call_id OR l.call_id = e.event_id)
		WHERE `+where+`
		ORDER BY CASE WHEN e.source = 'engine' THEN 0 ELSE 1 END, e.seq DESC`, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	type pair struct {
		selected string
		expected string
		callID   string
	}
	var pairs []pair
	set := map[string]bool{}
	seen := map[string]bool{}
	calls := map[string]bool{}
	for rows.Next() {
		var raw, source, callID, qid, expected string
		if rows.Scan(&raw, &source, &callID, &qid, &expected) != nil {
			continue
		}
		key := callID + "\x00" + qid
		if seen[key] {
			continue
		}
		var ev WorkEvent
		if json.Unmarshal([]byte(raw), &ev) != nil {
			continue
		}
		selected, ok := selectedForQuestion(ev.ModelResult, qid)
		if !ok {
			continue
		}
		seen[key] = true
		calls[callID] = true
		pairs = append(pairs, pair{selected: selected, expected: expected, callID: callID})
		set[selected] = true
		set[expected] = true
	}
	out.Labeled = int64(len(pairs))
	out.LabeledCalls = int64(len(calls))
	if len(pairs) == 0 {
		return out
	}
	labels := make([]string, 0, len(set))
	for l := range set {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	out.Labels = labels
	index := map[string]int{}
	for i, l := range labels {
		index[l] = i
	}
	out.Confusion = make([][]int64, len(labels))
	for i := range out.Confusion {
		out.Confusion[i] = make([]int64, len(labels))
	}
	var maeSum float64
	var maeN int
	for _, p := range pairs {
		sel := strings.TrimSpace(p.selected)
		exp := strings.TrimSpace(p.expected)
		if sel == "" {
			continue
		}
		if strings.EqualFold(sel, exp) {
			out.Matches++
		}
		if si, ok := index[sel]; ok {
			if ei, ok := index[exp]; ok {
				out.Confusion[ei][si]++
			}
		}
		if s, err1 := strconv.ParseFloat(sel, 64); err1 == nil {
			if e, err2 := strconv.ParseFloat(exp, 64); err2 == nil {
				d := s - e
				if d < 0 {
					d = -d
				}
				maeSum += d
				maeN++
			}
		}
	}
	out.Accuracy = float64(out.Matches) / float64(len(pairs))
	if maeN > 0 {
		v := maeSum / float64(maeN)
		out.MAE = &v
	}
	for _, label := range labels {
		i := index[label]
		var tp, pred, actual int64
		for j := range labels {
			tp += out.Confusion[i][j]
			pred += out.Confusion[i][j]
			actual += out.Confusion[j][i]
		}
		recall := 0.0
		if actual > 0 {
			recall = float64(tp) / float64(actual)
		}
		precision := 0.0
		if pred > 0 {
			precision = float64(tp) / float64(pred)
		}
		out.PerClass = append(out.PerClass, WorkClassMetric{Label: label, Precision: precision, Recall: recall, Support: actual})
	}
	return out
}

func (s *WorkStore) savings(f WorkFilter) []WorkSavings {
	where, args := f.where()
	rows, err := s.db.Query(`SELECT operation, evidence_json, model_result_json FROM events
		WHERE `+where+` AND kind='tool_result' AND operation IN ('laya_search','laya_read_relevant') ORDER BY seq DESC LIMIT 1000`, args...)
	if err != nil {
		return []WorkSavings{}
	}
	defer rows.Close()
	agg := map[string]*WorkSavings{}
	byChunk := map[string]*WorkSavings{}
	for rows.Next() {
		var op, evidence, result string
		if rows.Scan(&op, &evidence, &result) != nil {
			continue
		}
		m := mergeMaps(parseJSONMap(evidence), parseJSONMap(result))
		if nested, ok := m["savings"].(map[string]any); ok {
			m = mergeMaps(m, nested)
		}
		src, _ := number(m["source_bytes"])
		ret, _ := number(m["returned_bytes"])
		a := agg[op]
		if a == nil {
			a = &WorkSavings{Tool: op}
			agg[op] = a
		}
		a.Events++
		a.SourceBytes += int64(src)
		a.ReturnedBytes += int64(ret)
		for _, chunk := range chunkIDs(m) {
			if _, ok := byChunk[chunk]; !ok {
				byChunk[chunk] = a
			}
		}
	}
	// Дочитывания laya_get_chunk не создают новый «источник» и не сохраняют
	// текст повторно: они прибавляются как follow-up к исходному артефакту по
	// chunk_id. Если связь не найдена — показываем отдельной строкой.
	rows2, err := s.db.Query(`SELECT evidence_json FROM events
		WHERE `+where+` AND kind='tool_result' AND operation='laya_get_chunk' ORDER BY seq DESC LIMIT 2000`, args...)
	if err == nil {
		defer rows2.Close()
		for rows2.Next() {
			var evidence string
			if rows2.Scan(&evidence) != nil {
				continue
			}
			m := parseJSONMap(evidence)
			n, ok := number(m["returned_chars"])
			if !ok {
				n, _ = number(m["returned_bytes"])
			}
			chunks := chunkIDs(m)
			var target *WorkSavings
			for _, chunk := range chunks {
				if target = byChunk[chunk]; target != nil {
					break
				}
			}
			if target == nil {
				target = agg["laya_get_chunk"]
				if target == nil {
					target = &WorkSavings{Tool: "laya_get_chunk"}
					agg["laya_get_chunk"] = target
				}
				target.Events++
			}
			target.FollowupBytes += int64(n)
		}
	}
	out := make([]WorkSavings, 0, len(agg))
	for _, a := range agg {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tool < out[j].Tool })
	return out
}

func parseJSONMap(raw string) map[string]any {
	if raw == "" || raw == "null" {
		return map[string]any{}
	}
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return map[string]any{}
	}
	// evidence иногда приходит массивом: берём первый объект.
	if len(m) == 0 {
		var arr []map[string]any
		if json.Unmarshal([]byte(raw), &arr) == nil && len(arr) > 0 {
			return arr[0]
		}
	}
	return m
}

func mergeMaps(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if _, ok := out[k]; !ok {
			out[k] = v
		}
	}
	return out
}

func chunkIDs(m map[string]any) []string {
	var out []string
	add := func(v any) {
		switch x := v.(type) {
		case string:
			if x != "" {
				out = append(out, x)
			}
		case []any:
			for _, item := range x {
				if s, ok := item.(string); ok && s != "" {
					out = append(out, s)
				}
			}
		}
	}
	add(m["chunk_id"])
	add(m["stored_ids"])
	add(m["artifact_id"])
	if nested, ok := m["savings"].(map[string]any); ok {
		add(nested["chunk_id"])
		add(nested["stored_ids"])
	}
	return out
}

// ---------------------------------------------------------------- импорт

// WorkImportResult — итог импорта исторического JSONL.
type WorkImportResult struct {
	Imported   int `json:"imported"`
	Duplicates int `json:"duplicates"`
	Skipped    int `json:"skipped"`
}

// ImportJSONL импортирует старые metrics/decisions-*.jsonl. Текст входа в них не
// сохранялся, поэтому UI покажет «текст не сохранялся»; дубли отсекаются по
// хешу строки.
func (s *WorkStore) ImportJSONL(r io.Reader) (WorkImportResult, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	var batch []WorkEvent
	res := WorkImportResult{}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var row map[string]any
		if json.Unmarshal([]byte(line), &row) != nil {
			res.Skipped++
			continue
		}
		h := sha256.Sum256([]byte(line))
		key := hex.EncodeToString(h[:])[:24]
		kind := stringOf(row["kind"])
		action := stringOf(row["action"])
		status := "completed"
		if stringOf(row["inference_error"]) != "" {
			status = "fallback"
		}
		if action == "skip_mode" {
			status = "skipped"
		}
		ev := WorkEvent{
			SchemaVersion:  workSchemaVersion,
			EventID:        "imp_" + key,
			TraceID:        "imp_" + key,
			SessionID:      stringOf(row["session_id"]),
			Timestamp:      stringOf(row["ts"]),
			Source:         "import",
			Kind:           importKind(kind),
			Operation:      importOperation(kind, stringOf(row["tool"])),
			Status:         status,
			Model:          WorkModel{Checkpoint: stringOf(row["checkpoint"])},
			ModelResult:    json.RawMessage(mustJSON(map[string]any{"scores": row["scores"], "question_set_version": row["question_set_version"], "state_hash": row["state_hash"]})),
			PolicyResult:   json.RawMessage(mustJSON(map[string]any{"action": action, "reason": row["reason"], "mode": row["mode"], "det_hits": row["det_hits"], "laya_called": status != "skipped", "fallback": status == "fallback"})),
			Timing:         WorkTiming{InferenceMS: floatOf(row["elapsed_ms"]), TotalMS: floatOf(row["latency_ms"])},
			TextSaved:      boolPtr(false),
			Imported:       true,
			Truncated:      boolOf(row["state_truncated"]),
			ModelAttempted: status != "skipped" && (stringOf(row["checkpoint"]) != "" || row["elapsed_ms"] != nil),
		}
		ev.Input.TextSaved = boolPtr(false)
		if n, ok := number(row["state_chars"]); ok {
			ev.Input.Chars = int(n)
		}
		if err := ev.Normalize(s.cfg.MaxInputChars); err != nil {
			res.Skipped++
			continue
		}
		batch = append(batch, ev)
		if len(batch) >= 64 {
			ins, dup := s.insertBatch(batch)
			res.Imported += ins
			res.Duplicates += dup
			batch = batch[:0]
		}
	}
	if err := sc.Err(); err != nil {
		return res, err
	}
	if len(batch) > 0 {
		ins, dup := s.insertBatch(batch)
		res.Imported += ins
		res.Duplicates += dup
	}
	return res, nil
}

func importOperation(kind, tool string) string {
	if kind == "mcp_call" {
		return firstNonEmpty(tool, kind)
	}
	return firstNonEmpty(kind, tool)
}

func importKind(kind string) string {
	switch kind {
	case "risk_gate", "done_gate", "router":
		return "policy"
	case "mcp_call":
		return "tool_result"
	default:
		return "policy"
	}
}

func stringOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func floatOf(v any) float64 {
	if f, ok := number(v); ok {
		return f
	}
	return 0
}

func boolOf(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func boolPtr(v bool) *bool { return &v }

// SeedDemo вставляет детерминированный демонстрационный набор (только при
// явном -work-demo). В обычном режиме демо удаляется при старте.
func (s *WorkStore) SeedDemo() {
	now := time.Now().UTC()
	mk := func(i int, op, kind, status string, selected string, action string, laya bool, inf, total float64, demoInput string) WorkEvent {
		ev := WorkEvent{
			SchemaVersion:  workSchemaVersion,
			EventID:        fmt.Sprintf("demo_%02d", i),
			TraceID:        fmt.Sprintf("demo_trace_%02d", i),
			CallID:         fmt.Sprintf("demo_call_%02d", i),
			SessionID:      "demo-session",
			Timestamp:      now.Add(-time.Duration(20-i) * time.Minute).Format(time.RFC3339Nano),
			Source:         "plugin",
			Kind:           kind,
			Operation:      op,
			Status:         status,
			Model:          WorkModel{Checkpoint: "multilingual-fp16-0b095e005a4c", Device: "cuda"},
			Input:          WorkInput{OriginalRedacted: demoInput, EffectiveRedacted: demoInput},
			ModelResult:    json.RawMessage(mustJSON(map[string]any{"selected": selected, "score_semantics": "softmax_probability"})),
			PolicyResult:   json.RawMessage(mustJSON(map[string]any{"action": action, "laya_called": laya, "fallback": status == "fallback"})),
			Timing:         WorkTiming{InferenceMS: inf, TotalMS: total},
			ModelAttempted: kind == "model_call",
			Demo:           true,
		}
		return ev
	}
	events := []WorkEvent{
		mk(1, "risk_gate", "policy", "completed", "high", "ask", true, 27, 45, "Bash: git reset --hard && rm -rf build"),
		mk(2, "laya_search", "tool_result", "completed", "3", "rank", true, 21, 320, "Поиск: activeModes в plugin/src"),
		mk(3, "risk_gate", "policy", "skipped", "", "log", false, 0, 1, "Bash: ls -la (read-only)"),
		mk(4, "laya_decide", "model_call", "fallback", "", "fallback", false, 0, 1500, "Вопрос без ответа Laya"),
		mk(5, "laya_read_relevant", "tool_result", "completed", "2", "keep", true, 18, 90, "Чтение gates.mjs"),
	}
	for i := range events {
		_ = events[i].Normalize(s.cfg.MaxInputChars)
	}
	_, _ = s.insertBatch(events)
	_ = s.Label("demo_call_01", "risk", WorkLabel{Expected: "high", Author: "demo", Comment: "Опасная команда", Basis: "fixture"})
	_ = s.Label("demo_call_02", "rel", WorkLabel{Expected: "3", Author: "demo", Comment: "Прямое совпадение", Basis: "fixture"})
}
