// Package txn provides write-ahead logging, atomic stage commits with
// rollback, and provenance tracking for the media merge pipeline.
package txn

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Record kinds written to the WAL.
const (
	KindInput    = "input"    // input digest set for a stage
	KindBegin    = "begin"    // stage started
	KindTemp     = "temp"     // temporary output digest recorded
	KindCommit   = "commit"   // stage committed atomically
	KindRollback = "rollback" // uncommitted artifacts rolled back
)

// Record is one WAL entry. Sum chains the record content so a torn or
// corrupted tail is detected on replay.
type Record struct {
	Seq     int             `json:"seq"`
	Kind    string          `json:"kind"`
	Stage   string          `json:"stage"`
	Payload json.RawMessage `json:"payload"`
	Sum     string          `json:"sum"`
}

func (r *Record) checksum() string {
	h := sha256.New()
	fmt.Fprintf(h, "%d|%s|%s|", r.Seq, r.Kind, r.Stage)
	h.Write(r.Payload)
	return hex.EncodeToString(h.Sum(nil))
}

// WAL is an append-only log in the work directory.
type WAL struct {
	path string
	f    *os.File
	seq  int
}

// OpenWAL opens (creating if needed) the WAL inside dir.
func OpenWAL(dir string) (*WAL, error) {
	p := filepath.Join(dir, "wal.log")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	return &WAL{path: p, f: f}, nil
}

// Path returns the WAL file path.
func (w *WAL) Path() string { return w.path }

// Append writes one record and fsyncs before returning.
func (w *WAL) Append(kind, stage string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	w.seq++
	r := Record{Seq: w.seq, Kind: kind, Stage: stage, Payload: raw}
	r.Sum = r.checksum()
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err := w.f.Write(append(line, '\n')); err != nil {
		return err
	}
	return w.f.Sync()
}

// Replay reads all valid records, verifying checksums. On the first corrupt
// or torn record the file is truncated to the last valid boundary, so a
// crash mid-write never poisons recovery. seq is reset for further appends.
func (w *WAL) Replay() ([]Record, error) {
	if _, err := w.f.Seek(0, 0); err != nil {
		return nil, err
	}
	var out []Record
	var good int64 // offset just past the last valid record
	sc := bufio.NewScanner(w.f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	pos := int64(0)
	for sc.Scan() {
		line := sc.Bytes()
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			break // torn tail: truncate below
		}
		if r.Sum != r.checksum() {
			break // corrupted record: truncate below
		}
		out = append(out, r)
		pos += int64(len(line)) + 1
		good = pos
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	st, err := w.f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > good {
		if err := w.f.Truncate(good); err != nil {
			return nil, err
		}
	}
	if len(out) > 0 {
		w.seq = out[len(out)-1].Seq
	}
	if _, err := w.f.Seek(0, 2); err != nil {
		return nil, err
	}
	return out, nil
}

// Close closes the WAL file.
func (w *WAL) Close() error { return w.f.Close() }
