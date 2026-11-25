// Package txn implements write-ahead logging, atomic stage commit and
// rollback for the offline pipeline. Every stage first records an intent in
// the WAL, writes its artifacts to a private directory, then commits an
// immutable manifest. A crash before commit leaves recoverable (or cleanable)
// state; a crash after commit is durable.
package txn

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Stage names used across the pipeline, in execution order.
const (
	StageMetadata = "metadata"
	StageRecovery = "recovery"
	StageAlign    = "align"
	StageRender   = "render"
	StageReport   = "report"
)

// Stages is the canonical ordered list.
var Stages = []string{StageMetadata, StageRecovery, StageAlign, StageRender, StageReport}

// walEntry is one journal record. Only one of Begin/Commit/Abort is true.
type walEntry struct {
	Seq     int    `json:"seq"`
	Stage   string `json:"stage"`
	Begin   bool   `json:"begin,omitempty"`
	Commit  bool   `json:"commit,omitempty"`
	Abort   bool   `json:"abort,omitempty"`
	Intent  string `json:"intent,omitempty"` // planned output path, for cleanup
}

// WAL is an append-only journal backed by a single file. Writes are fsynced
// before control returns so a recovered process never loses a durable intent.
type WAL struct {
	mu   sync.Mutex
	path string
	f    *os.File
	seq  int
}

// OpenWAL opens (or creates) the journal inside dir and replays existing
// entries into memory so callers can inspect stage state immediately.
func OpenWAL(dir string) (*WAL, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("txn: create wal dir: %w", err)
	}
	p := filepath.Join(dir, "wal.log")
	// O_APPEND guarantees every write lands at end-of-file even after reads
	// via the same handle move the offset, so replay never corrupts the log.
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("txn: open wal: %w", err)
	}
	w := &WAL{path: p, f: f}
	entries, err := w.readAll()
	if err != nil {
		f.Close()
		return nil, err
	}
	if len(entries) > 0 {
		w.seq = entries[len(entries)-1].Seq
	}
	return w, nil
}

// readAll decodes every newline-delimited JSON entry. It reads the backing
// file directly and never mutates the handle's offset (the handle is opened
// O_APPEND, so writes are independent of read position).
func (w *WAL) readAll() ([]walEntry, error) {
	data, err := os.ReadFile(w.path)
	if err != nil {
		return nil, err
	}
	var entries []walEntry
	for i, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var e walEntry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("txn: corrupt WAL entry at line %d: %w", i+1, err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// append writes one entry, fsyncing so it survives a crash.
func (w *WAL) append(e walEntry) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seq++
	e.Seq = w.seq
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if _, err := w.f.Write(line); err != nil {
		return fmt.Errorf("txn: wal write: %w", err)
	}
	return w.f.Sync()
}

// Begin records a stage intent.
func (w *WAL) Begin(stage, intent string) error {
	return w.append(walEntry{Stage: stage, Begin: true, Intent: intent})
}

// Commit records a durable stage commit.
func (w *WAL) Commit(stage string) error {
	return w.append(walEntry{Stage: stage, Commit: true})
}

// Abort records a rollback.
func (w *WAL) Abort(stage string) error {
	return w.append(walEntry{Stage: stage, Abort: true})
}

// Close flushes and releases the journal file.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}

// StageState describes the journal outcome for one stage.
type StageState struct {
	Stage     string
	Committed bool
	Begun     bool
	Aborted   bool
	Intent    string
}

// States replays the journal and reduces it to per-stage state.
func (w *WAL) States() (map[string]StageState, error) {
	entries, err := w.readAll()
	if err != nil {
		return nil, err
	}
	out := map[string]StageState{}
	for _, e := range entries {
		s := out[e.Stage]
		s.Stage = e.Stage
		switch {
		case e.Begin:
			s.Begun = true
			s.Intent = e.Intent
		case e.Commit:
			s.Committed = true
		case e.Abort:
			s.Aborted = true
		}
		out[e.Stage] = s
	}
	return out, nil
}

// CommittedStages returns the ordered list of durably committed stages.
func (w *WAL) CommittedStages() ([]string, error) {
	states, err := w.States()
	if err != nil {
		return nil, err
	}
	var done []string
	for _, st := range Stages {
		if states[st].Committed {
			done = append(done, st)
		}
	}
	sort.Strings(done)
	return done, nil
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
