// Package txn provides WAL-journaled, atomically committed index builds
// with crash recovery inside a single output directory.
package txn

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Pipeline stages, in execution order.
const (
	StageParse  = "parse"
	StageMap    = "map"
	StageBuild  = "build"
	StageCommit = "commit"
)

// Stages lists all stages in execution order.
var Stages = []string{StageParse, StageMap, StageBuild, StageCommit}

// Record is one WAL entry: a stage begin or done marker.
type Record struct {
	Stage  string `json:"stage"`
	Status string `json:"status"` // "begin" or "done"
	Detail string `json:"detail,omitempty"`
}

// WAL is the append-only stage journal at work/wal.jsonl.
type WAL struct {
	path    string
	Records []Record
}

// OpenWAL loads the WAL under dir, returning an empty one if none exists.
func OpenWAL(dir string) (*WAL, error) {
	w := &WAL{path: filepath.Join(dir, "work", "wal.jsonl")}
	data, err := os.ReadFile(w.path)
	if errors.Is(err, fs.ErrNotExist) {
		return w, nil
	}
	if err != nil {
		return nil, err
	}
	for _, line := range splitLines(data) {
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, err
		}
		w.Records = append(w.Records, r)
	}
	return w, nil
}

// Log appends a stage record and fsyncs the journal.
func (w *WAL) Log(stage, status, detail string) error {
	r := Record{Stage: stage, Status: status, Detail: detail}
	if err := os.MkdirAll(filepath.Dir(w.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(r)
	if serr := f.Sync(); err == nil {
		err = serr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	w.Records = append(w.Records, r)
	return nil
}

// Done reports whether the stage has a "done" record.
func (w *WAL) Done(stage string) bool {
	for _, r := range w.Records {
		if r.Stage == stage && r.Status == "done" {
			return true
		}
	}
	return false
}

// Reset truncates the journal for a fresh build generation.
func (w *WAL) Reset() error {
	w.Records = nil
	return os.Remove(w.path)
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			if i > start {
				lines = append(lines, data[start:i])
			}
			start = i + 1
		}
	}
	if len(data) > start {
		lines = append(lines, data[start:])
	}
	return lines
}
