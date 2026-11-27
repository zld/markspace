// Package journal durably records processing commit points so an interrupted
// write can roll back to the last complete commit and reruns never commit an
// interval twice. All records are machine-readable JSON.
package journal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

const (
	dirName    = "journal"
	stateName  = "state.json"
	walName    = "wal.ndjson"
	tmpSuffix  = ".tmp"
)

// Record is one durable commit: one resolved interval of one track.
type Record struct {
	Key         string `json:"key"`          // idempotency key
	ProgramKey  string `json:"program_key"`
	TrackID     string `json:"track_id"`
	StartNS     int64  `json:"start_ns"`
	EndNS       int64  `json:"end_ns"`
	Source      string `json:"source"`
	Digest      string `json:"digest"`
	CommittedAt int64  `json:"committed_at_ns"`
}

// Rollback describes a recovery action after an interrupted write.
type Rollback struct {
	FromKey       string `json:"from_key"`
	FromWatermark int64  `json:"rollback_from_ns"`
	ToWatermark   int64  `json:"rollback_to_ns"`
	DiscardedWAL  int    `json:"discarded_wal_entries"`
}

// State is the on-disk durable snapshot.
type State struct {
	WatermarkNS int64    `json:"watermark_ns"`
	Committed   []Record `json:"committed"`
}

// Journal is the file-backed commit log for one run directory.
type Journal struct {
	mu       sync.Mutex
	dir      string
	state    State
	keys     map[string]struct{}
	wal      *os.File
	walPath  string
}

// Open loads or initializes a journal under outDir/journal. Any unconfirmed
// WAL entries left by a crashed process are discarded and a Rollback report
// is returned when recovery actually removed something.
func Open(outDir string) (*Journal, *Rollback, error) {
	dir := filepath.Join(outDir, dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, err
	}
	j := &Journal{dir: dir, keys: map[string]struct{}{}, walPath: filepath.Join(dir, walName)}
	statePath := filepath.Join(dir, stateName)
	if data, err := os.ReadFile(statePath); err == nil {
		if err := json.Unmarshal(data, &j.state); err != nil {
			return nil, nil, fmt.Errorf("corrupt journal state: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	for _, r := range j.state.Committed {
		j.keys[r.Key] = struct{}{}
	}
	rb, err := j.recoverWAL()
	if err != nil {
		return nil, nil, err
	}
	wal, err := os.OpenFile(j.walPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, err
	}
	j.wal = wal
	return j, rb, nil
}

// recoverWAL validates every line; a partial/final bad line means the write
// was interrupted and that prefix of unconfirmed work is rolled back.
func (j *Journal) recoverWAL() (*Rollback, error) {
	data, err := os.ReadFile(j.walPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lines := splitLines(data)
	good := 0
	var badFrom int64 = -1
	for i, ln := range lines {
		if len(ln) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(ln, &r); err != nil || r.Key == "" {
			badFrom = j.state.WatermarkNS
			break
		}
		good = i + 1
	}
	if badFrom < 0 && good == len(nonEmpty(lines)) {
		return nil, nil // clean shutdown, nothing pending
	}
	rb := &Rollback{FromWatermark: j.state.WatermarkNS, ToWatermark: j.state.WatermarkNS,
		DiscardedWAL: len(nonEmpty(lines)) - good}
	if len(j.state.Committed) > 0 {
		rb.FromKey = j.state.Committed[len(j.state.Committed)-1].Key
	}
	if err := os.WriteFile(j.walPath, nil, 0o644); err != nil {
		return nil, err
	}
	return rb, nil
}

// IsCommitted reports whether an interval key already durably committed.
func (j *Journal) IsCommitted(key string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	_, ok := j.keys[key]
	return ok
}

// Commit appends to the WAL and, once durable, folds the record into the
// atomic state snapshot. Re-committing an existing key is a no-op so reruns
// never double-commit an interval.
func (j *Journal) Commit(r Record) (bool, error) {
	if r.Key == "" {
		return false, errors.New("commit record requires a key")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, ok := j.keys[r.Key]; ok {
		return false, nil
	}
	enc, err := json.Marshal(r)
	if err != nil {
		return false, err
	}
	enc = append(enc, '\n')
	if _, err := j.wal.Write(enc); err != nil {
		return false, err
	}
	if err := j.wal.Sync(); err != nil {
		return false, err
	}
	j.state.Committed = append(j.state.Committed, r)
	j.keys[r.Key] = struct{}{}
	if r.EndNS > j.state.WatermarkNS {
		j.state.WatermarkNS = r.EndNS
	}
	if err := j.persistState(); err != nil {
		return false, err
	}
	// State snapshot is durable: the confirmed WAL prefix is no longer needed.
	if err := j.wal.Truncate(0); err != nil {
		return false, err
	}
	if _, err := j.wal.Seek(0, 0); err != nil {
		return false, err
	}
	return true, nil
}

// persistState writes state via a temp file + rename for atomic replacement.
func (j *Journal) persistState() error {
	sort.SliceStable(j.state.Committed, func(a, b int) bool {
		if j.state.Committed[a].StartNS != j.state.Committed[b].StartNS {
			return j.state.Committed[a].StartNS < j.state.Committed[b].StartNS
		}
		return j.state.Committed[a].Key < j.state.Committed[b].Key
	})
	data, err := json.MarshalIndent(&j.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(j.dir, stateName+tmpSuffix)
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(j.dir, stateName)); err != nil {
		return err
	}
	return nil
}

// Snapshot returns a copy of the durable state.
func (j *Journal) Snapshot() State {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := State{WatermarkNS: j.state.WatermarkNS, Committed: append([]Record(nil), j.state.Committed...)}
	return out
}

// Close flushes and releases the WAL handle.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.wal == nil {
		return nil
	}
	err := j.wal.Close()
	j.wal = nil
	return err
}

// SimulateInterruptedWrite appends a partial JSON line to the WAL without
// touching state, exactly mimicking a process crash mid-commit. The next
// Open on the same directory recovers and reports the rollback.
func (j *Journal) SimulateInterruptedWrite(partial Record) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	enc, err := json.Marshal(partial)
	if err != nil {
		return err
	}
	// Cut the line in half and omit the trailing newline deliberately.
	half := len(enc) / 2
	if _, err := j.wal.Write(enc[:half]); err != nil {
		return err
	}
	return j.wal.Sync()
}

func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			out = append(out, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}

func nonEmpty(lines [][]byte) [][]byte {
	out := lines[:0]
	for _, ln := range lines {
		if len(ln) > 0 {
			out = append(out, ln)
		}
	}
	return out
}
