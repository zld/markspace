// Package journal implements the write-ahead log that makes every
// pipeline stage recoverable. Records are append-only JSON lines,
// fsynced before any stage side effect is made visible, so a crash
// always leaves enough evidence to replay or roll back.
package journal

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// DirName is the journal directory inside a workspace.
const DirName = "journal"

// WALName is the append-only log file name.
const WALName = "wal.log"

// Record kinds.
const (
	KindBegin      = "begin"       // stage started; payload describes planned work
	KindPrepared   = "prepared"    // temp outputs written and hash-verified
	KindCommitted  = "committed"   // atomic replace completed
	KindRolledBack = "rolled_back" // temp outputs discarded
	KindAborted    = "aborted"     // stage failed before any visible output
)

// Record is one WAL entry. InputHash anchors the stage to its input;
// PlanHash/OutputHash make the committed work reproducible and
// verifiable on recovery. Files lists staged temporary paths.
type Record struct {
	Seq        int64             `json:"seq"`
	Kind       string            `json:"kind"`
	Stage      string            `json:"stage"`
	At         time.Time         `json:"at"`
	InputHash  string            `json:"input_hash,omitempty"`
	PlanHash   string            `json:"plan_hash,omitempty"`
	OutputHash string            `json:"output_hash,omitempty"`
	Files      []string          `json:"files,omitempty"`
	Meta       map[string]string `json:"meta,omitempty"`
}

// WAL is an opened append-only journal.
type WAL struct {
	path string
	f    *os.File
	w    *bufio.Writer
	seq  int64
}

// Open opens (creating if needed) the WAL inside workDir.
func Open(workDir string) (*WAL, error) {
	dir := filepath.Join(workDir, DirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, WALName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	w := &WAL{path: path, f: f, w: bufio.NewWriter(f)}
	if err := w.resumeSeq(); err != nil {
		f.Close()
		return nil, err
	}
	return w, nil
}

func (w *WAL) resumeSeq() error {
	records, err := ReadAll(w.path)
	if err != nil {
		return err
	}
	if n := len(records); n > 0 {
		w.seq = records[n-1].Seq
	}
	return nil
}

// Path returns the WAL file location.
func (w *WAL) Path() string { return w.path }

// append writes one record and fsyncs it before returning.
func (w *WAL) append(rec Record) (*Record, error) {
	w.seq++
	rec.Seq = w.seq
	if rec.At.IsZero() {
		rec.At = time.Now().UTC()
	}
	line, err := json.Marshal(rec)
	if err != nil {
		w.seq--
		return nil, err
	}
	if _, err := w.w.Write(append(line, '\n')); err != nil {
		return nil, err
	}
	if err := w.w.Flush(); err != nil {
		return nil, err
	}
	return &rec, w.f.Sync()
}

// Begin records the start of a stage with its input anchor.
func (w *WAL) Begin(stage, inputHash string, meta map[string]string) (*Record, error) {
	return w.append(Record{Kind: KindBegin, Stage: stage, InputHash: inputHash, Meta: cloneMeta(meta)})
}

// Prepared records verified temporary outputs and the plan hash.
func (w *WAL) Prepared(stage, planHash, outputHash string, files []string, meta map[string]string) (*Record, error) {
	return w.append(Record{Kind: KindPrepared, Stage: stage, PlanHash: planHash,
		OutputHash: outputHash, Files: append([]string(nil), files...), Meta: cloneMeta(meta)})
}

// Committed marks a stage as durably and atomically committed.
func (w *WAL) Committed(stage, outputHash string, files []string) (*Record, error) {
	return w.append(Record{Kind: KindCommitted, Stage: stage, OutputHash: outputHash,
		Files: append([]string(nil), files...)})
}

// RolledBack records discarded temporary outputs.
func (w *WAL) RolledBack(stage string, files []string) (*Record, error) {
	return w.append(Record{Kind: KindRolledBack, Stage: stage, Files: append([]string(nil), files...)})
}

// Aborted records a failure with no visible side effect.
func (w *WAL) Aborted(stage string, meta map[string]string) (*Record, error) {
	return w.append(Record{Kind: KindAborted, Stage: stage, Meta: cloneMeta(meta)})
}

// Close flushes and releases the file handle.
func (w *WAL) Close() error {
	if err := w.w.Flush(); err != nil {
		w.f.Close()
		return err
	}
	return w.f.Close()
}

// ReadAll parses a WAL file. A missing file yields no records. A
// truncated final line (classic hard-crash signature) is reported via
// the returned count so callers can treat the log as ending there.
func ReadAll(path string) ([]Record, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			return out, fmt.Errorf("journal: corrupt WAL line: %w", err)
		}
		out = append(out, rec)
	}
	return out, sc.Err()
}

// LastKind returns the latest record kind for a stage, or "" if absent.
func LastKind(records []Record, stage string) string {
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Stage == stage {
			return records[i].Kind
		}
	}
	return ""
}

// StageRecords returns the records of one stage in sequence order.
func StageRecords(records []Record, stage string) []Record {
	var out []Record
	for _, r := range records {
		if r.Stage == stage {
			out = append(out, r)
		}
	}
	return out
}

// Copy appends the content of src into the WAL stream (used by replay
// tooling that needs to re-anchor an existing journal).
func Copy(dst io.Writer, src io.Reader) (int64, error) { return io.Copy(dst, src) }

func cloneMeta(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out[k] = m[k]
	}
	return out
}
