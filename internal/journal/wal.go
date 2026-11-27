// Package journal implements the write-ahead log used by every pipeline stage.
//
// The log is append-only and line-delimited. Each record is
//
//	<sha256-hex-16> <json-payload>\n
//
// where the checksum covers the payload bytes only. A record whose checksum
// does not match is reported and skipped; a torn final line (no newline) is
// treated the same way. The log is never rewritten, so replay is idempotent and
// a crash can only ever truncate trailing records.
package journal

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"media.rebuild/internal/model"
)

// Record types.
const (
	TypeStageBegin    = "stage_begin"
	TypeStageDone     = "stage_done"
	TypeArtifact      = "artifact"
	TypeManifestDraft = "manifest_draft"
	TypeCommit        = "commit"
	TypeAbort         = "abort"
	TypeRecover       = "recover"
)

// Record is one log entry. Checksum is filled in by Append and verified by Replay.
type Record struct {
	Seq      int64           `json:"seq"`
	Type     string          `json:"type"`
	Task     string          `json:"task"`
	Stage    string          `json:"stage"`
	Time     string          `json:"time"`
	Payload  json.RawMessage `json:"payload,omitempty"`
	Checksum string          `json:"-"`
}

// Log is an open journal.
type Log struct {
	path string
	f    *os.File
	seq  int64
}

// Open opens or creates the journal at path for appending. The returned log's
// sequence counter resumes after the last durable record.
//
// A crash can leave a torn record at the tail: the bytes were written but the
// terminating newline was not, so the record never became durable. Appending
// after such a tail would glue the next record onto the partial line and make
// it unreadable forever, so Open truncates the file back to the last complete
// record before appending. Complete records are never touched.
func Open(path string) (*Log, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, model.New(model.CodeIO, model.Source{Name: path, Offset: -1}, "create journal directory", err)
	}
	if err := repairTail(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, model.New(model.CodeIO, model.Source{Name: path, Offset: -1}, "open journal", err)
	}
	l := &Log{path: path, f: f}
	recs, _, err := Replay(path, func(*Record) error { return nil })
	if err != nil {
		f.Close()
		return nil, err
	}
	if n := len(recs); n > 0 {
		l.seq = recs[n-1].Seq
	}
	return l, nil
}

// repairTail truncates a journal whose last record lacks a terminating
// newline. A missing file is fine: it is created by the append open.
func repairTail(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return model.New(model.CodeIO, model.Source{Name: path, Offset: -1}, "read journal for tail repair", err)
	}
	// A journal always ends every durable record with '\n'. If the final byte
	// is not a newline (or the file is empty), cut back to the last newline;
	// with no newline at all the whole file is one torn record.
	keep := bytes.LastIndexByte(data, '\n') + 1
	if keep == len(data) {
		return nil
	}
	if err := os.Truncate(path, int64(keep)); err != nil {
		return model.New(model.CodeIO, model.Source{Name: path, Offset: int64(keep)}, "truncate torn journal tail", err)
	}
	return nil
}

// Path returns the journal's file path.
func (l *Log) Path() string { return l.path }

// Close closes the log.
func (l *Log) Close() error {
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// Append writes one record durably. The record's Seq and Time are assigned
// here unless already set, so callers cannot produce non-monotonic sequences.
func (l *Log) Append(r Record) (*Record, error) {
	if r.Type == "" || r.Task == "" {
		return nil, model.New(model.CodeInvalidArgument, model.Source{Name: l.path, Offset: -1},
			"record needs type and task", nil)
	}
	l.seq++
	r.Seq = l.seq
	if r.Time == "" {
		r.Time = time.Now().UTC().Format(time.RFC3339Nano)
	}
	payload, err := json.Marshal(r)
	if err != nil {
		return nil, model.New(model.CodeIO, model.Source{Name: l.path, Offset: -1}, "encode record", err)
	}
	sum := sha256.Sum256(payload)
	line := fmt.Appendf(nil, "%s %s\n", hex.EncodeToString(sum[:8]), payload)
	off, err := l.f.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, model.New(model.CodeIO, model.Source{Name: l.path, Offset: -1}, "seek journal", err)
	}
	n, err := l.f.Write(line)
	if err != nil {
		return nil, model.New(model.CodeIO, model.Source{Name: l.path, Offset: off}, "append journal", err)
	}
	if err := l.f.Sync(); err != nil {
		return nil, model.New(model.CodeIO, model.Source{Name: l.path, Offset: off + int64(n)}, "sync journal", err)
	}
	out := r
	out.Checksum = hex.EncodeToString(sum[:8])
	return &out, nil
}

// Replay reads every record, invoking fn for each valid one. It returns the
// valid records in sequence order plus the corrupt records it encountered;
// corruption never aborts the replay.
func Replay(path string, fn func(*Record) error) ([]Record, []*model.Error, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, model.New(model.CodeIO, model.Source{Name: path, Offset: -1}, "open journal", err)
	}
	defer f.Close()
	return replay(f, path, fn)
}

func replay(r io.Reader, path string, fn func(*Record) error) ([]Record, []*model.Error, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	var recs []Record
	var bad []*model.Error
	offset := int64(0)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			lineOff := offset
			offset += int64(len(line))
			complete := line[len(line)-1] == '\n'
			if !complete {
				bad = append(bad, model.New(model.CodeJournalCorrupt, model.Source{Name: path, Offset: lineOff},
					"torn journal record without terminating newline", nil))
			} else if rec, ok := decodeLine(line[:len(line)-1], path, lineOff); ok {
				if fn != nil {
					if ferr := fn(rec); ferr != nil {
						return recs, bad, ferr
					}
				}
				recs = append(recs, *rec)
			} else {
				bad = append(bad, model.New(model.CodeJournalCorrupt, model.Source{Name: path, Offset: lineOff},
					"journal record failed checksum or decode", nil))
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return recs, bad, model.New(model.CodeIO, model.Source{Name: path, Offset: offset}, "read journal", err)
		}
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Seq < recs[j].Seq })
	model.SortErrors(bad)
	return recs, bad, nil
}

// decodeLine validates the checksum and decodes the JSON payload.
func decodeLine(line []byte, path string, off int64) (*Record, bool) {
	if len(line) < 18 || line[16] != ' ' {
		return nil, false
	}
	want := line[:16]
	payload := line[17:]
	sum := sha256.Sum256(payload)
	if !bytes.Equal(want, []byte(hex.EncodeToString(sum[:8]))) {
		return nil, false
	}
	var rec Record
	if err := json.Unmarshal(payload, &rec); err != nil {
		return nil, false
	}
	if rec.Seq <= 0 || rec.Type == "" {
		return nil, false
	}
	rec.Checksum = string(want)
	return &rec, true
}
