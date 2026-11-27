// Package txn implements the write-ahead log and atomic commit protocol that
// make the multi-stage pipeline recoverable. Each stage transition is fsynced
// before the stage's outputs are considered durable; a later failure can
// roll back to the most recent atomic commit point.
package txn

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	perr "gamepack/internal/errors"
)

const subsystem = "txn"

// Stage is one pipeline phase in strict migration order.
type Stage string

const (
	StageScan     Stage = "scan"
	StageAlign    Stage = "align"
	StageMux      Stage = "mux"
	StageVerify   Stage = "verify"
	StageCommit   Stage = "commit"
)

// OrderedStages is the legal migration sequence.
var OrderedStages = []Stage{StageScan, StageAlign, StageMux, StageVerify, StageCommit}

func stageIndex(s Stage) int {
	for i, x := range OrderedStages {
		if x == s {
			return i
		}
	}
	return -1
}

// State is the lifecycle state of a stage record.
type State string

const (
	StateStarted  State = "started"
	StateDone     State = "done"
	StateVerified State = "verified"
	StateFailed   State = "failed"
	StateRolledBack State = "rolled_back"
)

// Record is one WAL entry. Records are append-only; a stage is represented
// by its latest record.
type Record struct {
	Seq       int            `json:"seq"`
	Stage     Stage          `json:"stage"`
	State     State          `json:"state"`
	InputHash string         `json:"input_hash"`
	ResumeAt  string         `json:"resume_at"`
	Rollbacks int            `json:"rollbacks"`
	Detail    map[string]any `json:"detail,omitempty"`
}

// WAL is an append-only, mutex-guarded write-ahead log backed by one JSON
// line per record. It never stores bitstream payloads, only hashes and
// metadata, so logs cannot leak source content.
type WAL struct {
	mu       sync.Mutex
	path     string
	records  []Record
	nextSeq  int
}

// Open loads an existing WAL or creates an empty one at dir/wal.log.
func Open(dir string) (*WAL, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, perr.New(subsystem, perr.CodeTxn, "create txn dir: %v", err)
	}
	path := filepath.Join(dir, "wal.log")
	w := &WAL{path: path, nextSeq: 1}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return w, nil
		}
		return nil, perr.New(subsystem, perr.CodeTxn, "read wal: %v", err)
	}
	if err := w.load(raw); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *WAL) load(raw []byte) error {
	lines := splitLines(raw)
	for i, line := range lines {
		if len(line) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			return perr.At(subsystem, perr.CodeTxn, int64(i),
				"corrupt WAL record %d: %v", i, err).Wrap(err)
		}
		w.records = append(w.records, r)
		if r.Seq >= w.nextSeq {
			w.nextSeq = r.Seq + 1
		}
	}
	return nil
}

// Append validates the transition, records it and fsyncs the log before
// returning. Stage order must advance along OrderedStages; a jump backwards
// (outside an explicit rollback) is rejected.
func (w *WAL) Append(r Record) (Record, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if stageIndex(r.Stage) < 0 {
		return Record{}, perr.New(subsystem, perr.CodeTxn, "unknown stage %q", r.Stage)
	}
	if len(w.records) > 0 && r.State != StateRolledBack && r.State != StateFailed {
		last := w.records[len(w.records)-1]
		if stageIndex(r.Stage) < stageIndex(last.Stage) {
			return Record{}, perr.New(subsystem, perr.CodeTxn,
				"illegal backward transition %s -> %s", last.Stage, r.Stage)
		}
	}
	r.Seq = w.nextSeq
	w.nextSeq++
	if err := w.fsyncAppend(r); err != nil {
		return Record{}, err
	}
	w.records = append(w.records, r)
	return r, nil
}

func (w *WAL) fsyncAppend(r Record) error {
	line, err := json.Marshal(r)
	if err != nil {
		return perr.New(subsystem, perr.CodeTxn, "encode record: %v", err)
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return perr.New(subsystem, perr.CodeTxn, "open wal: %v", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return perr.New(subsystem, perr.CodeTxn, "write wal: %v", err)
	}
	if err := f.Sync(); err != nil {
		return perr.New(subsystem, perr.CodeTxn, "fsync wal: %v", err)
	}
	return nil
}

// Records returns a copy of all WAL records.
func (w *WAL) Records() []Record {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]Record, len(w.records))
	copy(out, w.records)
	return out
}

// LastVerified returns the highest stage whose latest state is verified, and
// its record. Recovery resumes from the stage following it.
func (w *WAL) LastVerified() (Stage, *Record) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var best Stage = ""
	var bestRec *Record
	for i := range w.records {
		r := &w.records[i]
		if r.State == StateVerified || r.State == StateDone {
			if best == "" || stageIndex(r.Stage) > stageIndex(best) {
				best = r.Stage
				rec := *r
				bestRec = &rec
			}
		}
	}
	return best, bestRec
}

// RollbackCount sums explicit rollback records for reporting.
func (w *WAL) RollbackCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, r := range w.records {
		if r.State == StateRolledBack {
			n++
		}
	}
	return n
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}

// String gives a compact human-readable stage summary for terminal output.
func (r Record) String() string {
	return fmt.Sprintf("#%d %s/%s resume=%s", r.Seq, r.Stage, r.State, r.ResumeAt)
}
