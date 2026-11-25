// Package txn implements deterministic recovery: every pipeline stage writes a
// write-ahead log record before touching its temp output, commits atomically,
// and can only resume when the input digest, WAL stage and committed outputs
// all agree.
package txn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"mediad/internal/timeline"
)

// Stage names in pipeline order. They are persisted verbatim.
const (
	StageParse      = "parse"
	StageTimeline   = "timeline"
	StageAnalyze    = "analyze"
	StageFingerprint = "fingerprint"
	StageCommit     = "commit"
)

// Stages is the ordered list used by the pipeline.
var Stages = []string{StageParse, StageTimeline, StageAnalyze, StageFingerprint, StageCommit}

// Record is one WAL entry.
type Record struct {
	Seq       int    `json:"seq"`
	Stage     string `json:"stage"`
	InputSHA  string `json:"input_sha256"`
	TempFile  string `json:"temp_file,omitempty"`
	TempSHA   string `json:"temp_sha256,omitempty"`
	Committed bool   `json:"committed"`
	TimeMS    int64  `json:"time_ms"`
}

// privateFileMode is owner read/write only, as required for temp files.
const privateFileMode os.FileMode = 0o600
const privateDirMode os.FileMode = 0o700

// WAL is an append-only write-ahead log backed by a single JSON-lines file.
type WAL struct {
	path    string
	dir     string
	inputSHA string
	file    *os.File
	records []Record
}

// CreateWAL opens (or creates) the WAL inside dir. inputSHA binds every record
// to one specific input so a mismatched rerun can be refused.
func CreateWAL(dir, inputSHA string) (*WAL, error) {
	if err := os.MkdirAll(dir, privateDirMode); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "wal.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, privateFileMode)
	if err != nil {
		return nil, err
	}
	w := &WAL{path: path, dir: dir, inputSHA: inputSHA, file: f}
	if err := w.load(); err != nil {
		f.Close()
		return nil, err
	}
	return w, nil
}

func (w *WAL) load() error {
	data, err := os.ReadFile(w.path)
	if err != nil {
		return err
	}
	start := 0
	for start < len(data) {
		nl := indexByte(data, start, '\n')
		if nl < 0 {
			nl = len(data)
		}
		line := data[start:nl]
		start = nl + 1
		if len(line) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			return timeline.New(timeline.ErrRecovery, w.path, -1, "corrupt WAL record: %v", err)
		}
		w.records = append(w.records, rec)
	}
	return nil
}

func indexByte(b []byte, from int, c byte) int {
	for i := from; i < len(b); i++ {
		if b[i] == c {
			return i
		}
	}
	return -1
}

// Path returns the WAL file location.
func (w *WAL) Path() string { return w.path }

// InputSHA returns the digest this WAL is bound to.
func (w *WAL) InputSHA() string { return w.inputSHA }

// Records returns a copy of all loaded records.
func (w *WAL) Records() []Record {
	out := make([]Record, len(w.records))
	copy(out, w.records)
	return out
}

// LastCommittedStage returns the latest stage whose commit record is present,
// or "" when nothing committed yet.
func (w *WAL) LastCommittedStage() string {
	last := ""
	for _, r := range w.records {
		if r.Committed {
			last = r.Stage
		}
	}
	return last
}

// Append writes one record durably (fsync) and returns it.
func (w *WAL) Append(rec Record) (Record, error) {
	if rec.InputSHA == "" {
		rec.InputSHA = w.inputSHA
	}
	if rec.InputSHA != w.inputSHA {
		return Record{}, timeline.New(timeline.ErrRecovery, w.path, -1,
			"WAL input digest %s does not match current %s", rec.InputSHA, w.inputSHA)
	}
	rec.Seq = len(w.records) + 1
	line, err := json.Marshal(rec)
	if err != nil {
		return Record{}, err
	}
	line = append(line, '\n')
	if _, err := w.file.Write(line); err != nil {
		return Record{}, err
	}
	if err := w.file.Sync(); err != nil {
		return Record{}, err
	}
	w.records = append(w.records, rec)
	return rec, nil
}

// Close flushes and closes the WAL file.
func (w *WAL) Close() error {
	if w.file == nil {
		return nil
	}
	err := w.file.Sync()
	if cErr := w.file.Close(); err == nil {
		err = cErr
	}
	w.file = nil
	return err
}

// TempPath returns a private temp file path inside the WAL directory.
func (w *WAL) TempPath(name string) string {
	return filepath.Join(w.dir, "tmp-"+name)
}

// FileSHA256 hashes a file's contents.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// BytesSHA256 hashes raw bytes.
func BytesSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
