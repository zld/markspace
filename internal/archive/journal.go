// Package archive journals every stage before writing, commits
// artifacts atomically, and recovers from the last committed boundary.
package archive

import (
	"encoding/json"
	"os"
	"path/filepath"

	merr "medialib/internal/errors"
)

// Entry is one journal record. Op is "begin", "commit" or "rollback".
type Entry struct {
	Seq      int    `json:"seq"`
	Stage    string `json:"stage"`
	Op       string `json:"op"`
	Artifact string `json:"artifact"`
	Hash     string `json:"hash,omitempty"`
}

// Journal appends fsynced JSON-lines records to journal.log.
type Journal struct {
	path    string
	f       *os.File
	entries []Entry
}

// OpenJournal creates or replays the journal inside dir.
func OpenJournal(dir string) (*Journal, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, merr.New(merr.StageJournal, -1, "mkdir", err)
	}
	j := &Journal{path: filepath.Join(dir, "journal.log")}
	data, err := os.ReadFile(j.path)
	if err != nil && !os.IsNotExist(err) {
		return nil, merr.New(merr.StageJournal, -1, "read", err)
	}
	for i, line := range splitLines(data) {
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, merr.New(merr.StageJournal, int64(i), "replay", err)
		}
		j.entries = append(j.entries, e)
	}
	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, merr.New(merr.StageJournal, -1, "open", err)
	}
	j.f = f
	return j, nil
}

// Append writes and fsyncs one record before returning.
func (j *Journal) Append(stage, op, artifact, hash string) error {
	e := Entry{Seq: len(j.entries), Stage: stage, Op: op, Artifact: artifact, Hash: hash}
	line, err := json.Marshal(e)
	if err != nil {
		return merr.New(merr.StageJournal, -1, "marshal", err)
	}
	if _, err := j.f.Write(append(line, '\n')); err != nil {
		return merr.New(merr.StageJournal, int64(e.Seq), "write", err)
	}
	if err := j.f.Sync(); err != nil {
		return merr.New(merr.StageJournal, int64(e.Seq), "fsync", err)
	}
	j.entries = append(j.entries, e)
	return nil
}

// Entries returns the replayed and newly appended records.
func (j *Journal) Entries() []Entry { return j.entries }

// Close flushes and closes the journal file.
func (j *Journal) Close() error {
	if j.f == nil {
		return nil
	}
	err := j.f.Close()
	j.f = nil
	return err
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			if i > start {
				out = append(out, b[start:i])
			}
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}
