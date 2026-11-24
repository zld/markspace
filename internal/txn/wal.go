// Package txn implements write-ahead logging, atomic file commit, recovery,
// and rollback for medpipe outputs.
package txn

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"medpipe/internal/fault"
)

const (
	// MaxWALBytes bounds write-ahead log growth.
	MaxWALBytes = 4 << 20
	// WALName is the deterministic log filename in an output directory.
	WALName = "medpipe.wal.jsonl"
)

// State is a transaction state in the WAL.
type State string

const (
	StateStarted    State = "started"
	StatePrepared   State = "prepared"
	StateCommitted  State = "committed"
	StateRolledBack State = "rolled_back"
)

// Record is one JSONL WAL entry.
type Record struct {
	Seq           int64     `json:"seq"`
	Time          time.Time `json:"time"`
	TxnID         string    `json:"txn_id"`
	Stage         string    `json:"stage"`
	State         State     `json:"state"`
	InputSHA256   string    `json:"input_sha256,omitempty"`
	MappingSHA256 string    `json:"mapping_sha256,omitempty"`
	TempPath      string    `json:"temp_path,omitempty"`
	FinalPath     string    `json:"final_path,omitempty"`
	OutputSHA256  string    `json:"output_sha256,omitempty"`
	OutputSize    int64     `json:"output_size,omitempty"`
	CommitMarker  string    `json:"commit_marker,omitempty"`
	Message       string    `json:"message,omitempty"`
}

// WAL is an append-only JSON lines write-ahead log.
type WAL struct {
	path string
	f    *os.File
	seq  int64
}

// OpenWAL opens or creates a bounded WAL.
func OpenWAL(path string) (*WAL, *fault.Error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fault.Wrap(fault.ErrWAL, "txn.wal.mkdir", 0, path, err)
	}
	if st, err := os.Stat(path); err == nil && st.Size() > MaxWALBytes {
		return nil, fault.New(fault.ErrLogTooLarge, "txn.wal.limit", 0, "WAL exceeds size limit")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fault.Wrap(fault.ErrWAL, "txn.wal.open", 0, path, err)
	}
	w := &WAL{path: path, f: f}
	recs, ferr := w.ReadRecords()
	if ferr != nil {
		_ = f.Close()
		return nil, ferr
	}
	if len(recs) > 0 {
		w.seq = recs[len(recs)-1].Seq
	}
	return w, nil
}

// Path returns the WAL path.
func (w *WAL) Path() string { return w.path }

// Append writes and fsyncs one record before the corresponding stage proceeds.
func (w *WAL) Append(r Record) (*Record, *fault.Error) {
	if w == nil || w.f == nil {
		return nil, fault.New(fault.ErrWAL, "txn.wal.append", 0, "closed WAL")
	}
	if st, err := w.f.Stat(); err == nil && st.Size() > MaxWALBytes {
		return nil, fault.New(fault.ErrLogTooLarge, "txn.wal.limit", st.Size(), "WAL exceeds size limit")
	}
	w.seq++
	r.Seq = w.seq
	if r.Time.IsZero() {
		r.Time = time.Now().UTC()
	}
	b, err := json.Marshal(r)
	if err != nil {
		return nil, fault.Wrap(fault.ErrWAL, "txn.wal.encode", 0, "encode record", err)
	}
	b = append(b, '\n')
	if _, err := w.f.Write(b); err != nil {
		return nil, fault.Wrap(fault.ErrWAL, "txn.wal.write", 0, "append record", err)
	}
	if err := w.f.Sync(); err != nil {
		return nil, fault.Wrap(fault.ErrWAL, "txn.wal.sync", 0, "sync WAL", err)
	}
	return &r, nil
}

// Close closes the file.
func (w *WAL) Close() error {
	if w == nil || w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// ReadRecords reads all complete JSON records. A torn final line is truncated
// so recovery can proceed from the last durable record.
func (w *WAL) ReadRecords() ([]Record, *fault.Error) {
	if w == nil {
		return nil, fault.New(fault.ErrWAL, "txn.wal.read", 0, "nil WAL")
	}
	if _, err := w.f.Seek(0, io.SeekStart); err != nil {
		return nil, fault.Wrap(fault.ErrWAL, "txn.wal.seek", 0, "seek WAL", err)
	}
	data, err := io.ReadAll(io.LimitReader(w.f, MaxWALBytes+1))
	if err != nil {
		return nil, fault.Wrap(fault.ErrWAL, "txn.wal.read", 0, "read WAL", err)
	}
	if int64(len(data)) > MaxWALBytes {
		return nil, fault.New(fault.ErrLogTooLarge, "txn.wal.limit", 0, "WAL exceeds size limit")
	}
	var records []Record
	s := bufio.NewScanner(bytes.NewReader(data))
	s.Buffer(make([]byte, 0, 4096), MaxWALBytes)
	for s.Scan() {
		line := bytes.TrimSpace(s.Bytes())
		if len(line) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			// A complete invalid line is corruption, not a torn append. Torn
			// appends normally have no trailing newline and are handled below.
			if bytes.HasSuffix(data, []byte("\n")) {
				return records, fault.Wrap(fault.ErrWAL, "txn.wal.decode", 0, "invalid WAL record", err)
			}
			break
		}
		records = append(records, r)
	}
	if err := s.Err(); err != nil {
		return records, fault.Wrap(fault.ErrWAL, "txn.wal.scan", 0, "scan WAL", err)
	}
	// Truncate a torn final partial line if necessary.
	if n := lastCompleteOffset(data); n >= 0 && int64(n) < int64(len(data)) {
		if err := os.Truncate(w.path, int64(n)); err != nil {
			return records, fault.Wrap(fault.ErrWAL, "txn.wal.truncate", int64(n), "truncate torn WAL", err)
		}
	}
	return records, nil
}

func lastCompleteOffset(data []byte) int {
	i := bytes.LastIndexByte(data, '\n')
	if i < 0 {
		return 0
	}
	return i + 1
}

// ReadWALRecords is a convenience reader for recovery without an open WAL.
func ReadWALRecords(path string) ([]Record, *fault.Error) {
	w, err := OpenWAL(path)
	if err != nil {
		return nil, err
	}
	defer w.Close()
	return w.ReadRecords()
}
