// Package store persists DAG checkpoints in a local journal using atomic
// temp-file-and-rename writes. A journal is only reused when its input
// digest matches the current run; corrupt journals are quarantined rather
// than trusted, forcing recomputation from safe predecessor nodes.
package store

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"mediasync/internal/clock"
)

// Entry is one persisted node checkpoint.
type Entry struct {
	Key         string          `json:"key"`
	Node        string          `json:"node"`
	InputDigest string          `json:"input_digest"`
	DepKeys     []string        `json:"dep_keys"`
	Completed   bool            `json:"completed"`
	Output      json.RawMessage `json:"output"`
}

type journalFile struct {
	InputDigest string            `json:"input_digest"`
	Entries     map[string]*Entry `json:"entries"`
}

// Journal is a durable, concurrency-safe CheckpointStore.
type Journal struct {
	mu          sync.Mutex
	path        string
	inputDigest string
	data        journalFile
	quarantined string // non-empty when an existing journal was isolated
}

// NewJournal opens (or creates) the journal at path. inputDigest must
// identify the exact input summary of the current run: entries carrying a
// different digest are never returned by Load. A journal that cannot be
// parsed is moved aside to a ".bad" file and an empty journal starts fresh.
func NewJournal(path, inputDigest string) (*Journal, error) {
	if path == "" || inputDigest == "" {
		return nil, clock.NewError(clock.ErrInvalidArgument, "journal path and input digest are required")
	}
	j := &Journal{path: path, inputDigest: inputDigest,
		data: journalFile{InputDigest: inputDigest, Entries: map[string]*Entry{}}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return j, nil
	}
	if err != nil {
		return nil, clock.AsMediaError(err)
	}
	if perr := json.Unmarshal(raw, &j.data); perr != nil {
		if qerr := j.quarantineFile(raw); qerr != nil {
			return nil, qerr
		}
		j.data = journalFile{InputDigest: inputDigest, Entries: map[string]*Entry{}}
		return j, nil
	}
	if j.data.Entries == nil {
		j.data.Entries = map[string]*Entry{}
	}
	return j, nil
}

// quarantine moves an unparseable journal aside so no later run can trust
// it, keeping the bytes for forensics.
func (j *Journal) quarantineFile(raw []byte) error {
	bad := j.path + ".bad"
	for n := 0; ; n++ {
		candidate := bad
		if n > 0 {
			candidate = filepath.Join(filepath.Dir(bad),
				filepath.Base(bad)+"."+itoa(n))
		}
		if _, err := os.Stat(candidate); errors.Is(err, fs.ErrNotExist) {
			bad = candidate
			break
		}
	}
	if err := os.WriteFile(bad, raw, 0o600); err != nil {
		return clock.AsMediaError(err)
	}
	if err := os.Remove(j.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return clock.AsMediaError(err)
	}
	j.quarantined = bad
	return nil
}

// QuarantinedPath reports where a corrupt journal was moved, if any.
func (j *Journal) QuarantinedPath() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.quarantined
}

// Load returns a checkpoint only when the key exists, is completed, was
// produced from the same input digest, and all its dependency checkpoints
// are themselves present and completed.
func (j *Journal) Load(key string) (any, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	e, ok := j.data.Entries[key]
	if !ok || !e.Completed || e.InputDigest != j.inputDigest {
		return nil, false
	}
	for _, dk := range e.DepKeys {
		de, ok := j.data.Entries[dk]
		if !ok || !de.Completed || de.InputDigest != j.inputDigest {
			return nil, false // dependency missing/unfinished: recompute
		}
	}
	out := make(json.RawMessage, len(e.Output))
	copy(out, e.Output)
	return out, true
}

// Store persists an output for key. Writes are idempotent on key and made
// durable by writing a sibling temp file, fsyncing it and renaming it over
// the journal, so a crash can never leave a half-written journal behind.
func (j *Journal) Store(key string, output any) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	payload, err := json.Marshal(output)
	if err != nil {
		return clock.AsMediaError(err)
	}
	e, exists := j.data.Entries[key]
	if !exists {
		e = &Entry{Key: key}
		j.data.Entries[key] = e
	}
	e.Output = payload
	e.Completed = true
	e.InputDigest = j.inputDigest
	return j.persistLocked()
}

// RecordNode satisfies dag.AnnotatingStore by annotating how a key was
// derived: the node name and the completed dependency checkpoint keys.
func (j *Journal) RecordNode(key, node string, depKeys []string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	e := j.data.Entries[key]
	if e == nil {
		return clock.NewError(clock.ErrInvalidArgument, "no checkpoint for key %s", key)
	}
	e.Node = node
	e.DepKeys = append([]string(nil), depKeys...)
	return j.persistLocked()
}

func (j *Journal) persistLocked() error {
	// Stable key ordering keeps on-disk bytes deterministic across runs.
	keys := make([]string, 0, len(j.data.Entries))
	for k := range j.data.Entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := journalFile{InputDigest: j.data.InputDigest,
		Entries: make(map[string]*Entry, len(keys))}
	for _, k := range keys {
		ordered.Entries[k] = j.data.Entries[k]
	}
	raw, err := json.MarshalIndent(ordered, "", "  ")
	if err != nil {
		return clock.AsMediaError(err)
	}
	dir := filepath.Dir(j.path)
	tmp, err := os.CreateTemp(dir, ".journal-*.tmp")
	if err != nil {
		return clock.AsMediaError(err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return clock.AsMediaError(err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return clock.AsMediaError(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return clock.AsMediaError(err)
	}
	if err := os.Rename(tmpName, j.path); err != nil {
		os.Remove(tmpName)
		return clock.AsMediaError(err)
	}
	return nil
}

// Size returns the number of persisted entries.
func (j *Journal) Size() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.data.Entries)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
