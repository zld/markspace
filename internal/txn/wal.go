// Package txn implements append-only write-ahead logging, staged index
// preparation and atomic commit/recovery boundaries.
package txn

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"assetcheck/internal/errmodel"
)

// WALFileName is the append-only log inside the index directory.
const WALFileName = "wal.log"

// Record types.
const (
	RecBegin        = "begin"
	RecStage        = "stage"
	RecPrepareIndex = "prepare-index"
	RecCommit       = "commit"
	RecApplied      = "applied"
	RecRollback     = "rollback"
)

// Record is one WAL entry.
type Record struct {
	LSN        uint64 `json:"lsn"`
	TxnID      string `json:"txn"`
	Type       string `json:"type"`
	Stage      string `json:"stage,omitempty"`
	Checkpoint string `json:"checkpoint,omitempty"`
	Seq        uint64 `json:"seq,omitempty"`
	Key        string `json:"key,omitempty"`
	Staging    string `json:"staging,omitempty"`
	Final      string `json:"final,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// WAL is a JSON-lines append-only log.
type WAL struct {
	path string
	mu   sync.Mutex
	f    *os.File
	lsn  uint64
}

// OpenWAL opens (creating if needed) the WAL and replays its LSN counter.
func OpenWAL(dir string) (*WAL, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, errmodel.New(errmodel.ErrWALWrite, "mkdir wal dir: %v", err)
	}
	path := filepath.Join(dir, WALFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, errmodel.New(errmodel.ErrWALWrite, "open wal: %v", err)
	}
	w := &WAL{path: path, f: f}
	recs, err := w.ReadAll()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	for _, r := range recs {
		if r.LSN > w.lsn {
			w.lsn = r.LSN
		}
	}
	return w, nil
}

// Close releases the file handle.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}

// Append durably appends one record, assigning the next LSN.
func (w *WAL) Append(r Record) (Record, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lsn++
	r.LSN = w.lsn
	b, err := json.Marshal(r)
	if err != nil {
		return r, errmodel.New(errmodel.ErrWALWrite, "marshal record: %v", err)
	}
	if _, err := w.f.Write(append(b, '\n')); err != nil {
		return r, errmodel.New(errmodel.ErrWALWrite, "append wal: %v", err)
	}
	if err := w.f.Sync(); err != nil {
		return r, errmodel.New(errmodel.ErrWALWrite, "fsync wal: %v", err)
	}
	return r, nil
}

// ReadAll replays every WAL record in order.
func (w *WAL) ReadAll() ([]Record, error) {
	f, err := os.Open(w.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, errmodel.New(errmodel.ErrWALCorrupt, "reopen wal: %v", err)
	}
	defer f.Close()
	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		raw := sc.Bytes()
		if len(raw) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(raw, &r); err != nil {
			return out, errmodel.New(errmodel.ErrWALCorrupt,
				"wal line %d invalid JSON: %v", line, err)
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return out, errmodel.New(errmodel.ErrWALCorrupt, "read wal: %v", err)
	}
	return out, nil
}

// LastCommitSeq returns the greatest seq seen in commit records.
func LastCommitSeq(recs []Record) uint64 {
	var seq uint64
	for _, r := range recs {
		if r.Type == RecCommit && r.Seq > seq {
			seq = r.Seq
		}
	}
	return seq
}
