// Package txn implements write-ahead logging, atomic stage commits and
// crash recovery for the media ingest pipeline. All paths are relative to a
// single project temp directory; no absolute system paths are ever recorded.
package txn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Stage states recorded by the write-ahead log.
type StageState string

const (
	StateLogged    StageState = "logged"    // WAL appended, work may begin
	StatePrepared  StageState = "prepared"  // temp artifacts written and hashed
	StateCommitted StageState = "committed" // atomically published
	StateAborted   StageState = "aborted"   // rolled back to prior boundary
)

// Entry is one WAL record. JSON keys are fixed so two identical runs produce
// byte-identical journals (no wall-clock timestamps are stored).
type Entry struct {
	Seq        int        `json:"seq"`
	Stage      string     `json:"stage"`
	State      StageState `json:"state"`
	InputHash  string     `json:"input_hash"`
	OutputHash string     `json:"output_hash,omitempty"`
	RelPath    string     `json:"rel_path"`
}

// ErrUnsafePath rejects journal paths that escape the project directory.
var ErrUnsafePath = errors.New("unsafe journal path")

func safePath(base, rel string) (string, error) {
	if filepath.IsAbs(rel) || strings.Contains(rel, "..") {
		return "", fmt.Errorf("%w: %q", ErrUnsafePath, rel)
	}
	clean := filepath.Clean(filepath.Join(base, rel))
	if !strings.HasPrefix(clean, filepath.Clean(base)+string(os.PathSeparator)) && clean != filepath.Clean(base) {
		return "", fmt.Errorf("%w: %q escapes project root", ErrUnsafePath, rel)
	}
	return clean, nil
}

// HashBytes returns the hex SHA-256 of a byte slice.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// HashFile returns the hex SHA-256 of a file's contents.
func HashFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return HashBytes(b), nil
}

// Journal appends entries to a single append-only file and keeps an in-memory
// mirror for replay decisions.
type Journal struct {
	dir     string
	file    *os.File
	entries []Entry
}

const journalName = "journal.wal"

// OpenJournal opens (or creates) the WAL inside baseDir and replays existing
// records so callers immediately see the last durable state.
func OpenJournal(baseDir string) (*Journal, error) {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, err
	}
	j := &Journal{dir: baseDir}
	path := filepath.Join(baseDir, journalName)
	if existing, err := os.ReadFile(path); err == nil && len(existing) > 0 {
		if err := j.replay(existing); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	j.file = f
	return j, nil
}

func (j *Journal) replay(raw []byte) error {
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return fmt.Errorf("corrupt journal: %w", err)
		}
		j.entries = append(j.entries, e)
	}
	return nil
}

// Dir reports the bounded project temp directory (relative-safe).
func (j *Journal) Dir() string { return j.dir }

// append writes one durable JSON line and flushes it.
func (j *Journal) append(e Entry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err := j.file.Write(b); err != nil {
		return err
	}
	if err := j.file.Sync(); err != nil {
		return err
	}
	j.entries = append(j.entries, e)
	return nil
}

// Log records the intent to run a stage before any artifact is written.
func (j *Journal) Log(seq int, stage, inputHash, relPath string) (Entry, error) {
	if _, err := safePath(j.dir, relPath); err != nil {
		return Entry{}, err
	}
	e := Entry{Seq: seq, Stage: stage, State: StateLogged, InputHash: inputHash, RelPath: relPath}
	return e, j.append(e)
}

// Prepare records the hash of fully written but not yet committed temp output.
func (j *Journal) Prepare(seq int, outputHash string) error {
	return j.append(j.latest(seq, StatePrepared, outputHash))
}

// Commit marks a prepared stage as durably published.
func (j *Journal) Commit(seq int) error {
	return j.append(j.latest(seq, StateCommitted, ""))
}

// Abort records a rollback marker.
func (j *Journal) Abort(seq int) error {
	return j.append(j.latest(seq, StateAborted, ""))
}

func (j *Journal) latest(seq int, state StageState, outHash string) Entry {
	for i := len(j.entries) - 1; i >= 0; i-- {
		if j.entries[i].Seq == seq {
			e := j.entries[i]
			e.State = state
			if outHash != "" {
				e.OutputHash = outHash
			}
			return e
		}
	}
	return Entry{Seq: seq, State: state, OutputHash: outHash}
}

// Entries returns a copy of the replayed/appended records.
func (j *Journal) Entries() []Entry {
	out := make([]Entry, len(j.entries))
	copy(out, j.entries)
	return out
}

// Close flushes and releases the WAL file handle.
func (j *Journal) Close() error { return j.file.Close() }

// LastCommitted returns the highest seq with a committed marker, or -1.
func (j *Journal) LastCommitted() int {
	last := -1
	for _, e := range j.entries {
		if e.State == StateCommitted && e.Seq > last {
			last = e.Seq
		}
	}
	return last
}

// SortedStages returns stage names in committed order for boundary reporting.
func (j *Journal) SortedStages() []string {
	seen := map[string]bool{}
	var out []string
	es := j.Entries()
	sort.Slice(es, func(i, k int) bool { return es[i].Seq < es[k].Seq })
	for _, e := range es {
		if e.State == StateCommitted && !seen[e.Stage] {
			seen[e.Stage] = true
			out = append(out, e.Stage)
		}
	}
	return out
}
