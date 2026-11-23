// Package checkpoint persists terminal scheduler nodes so an interrupted run
// resumes without repeating acknowledged work. Every write is a temporary file
// followed by an atomic rename, and every entry is validated against a format
// version and its own digest.
package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"archivecheck/internal/model"
	"archivecheck/internal/scheduler"
)

// Version is the on-disk format version. A store written by a different
// version is rejected rather than silently misread.
const Version = 2

// FileName is the checkpoint file name inside the state directory.
const FileName = "checkpoint.json"

// Entry is one terminal node.
type Entry struct {
	// Key is the idempotency-key digest.
	Key string `json:"key"`
	// NodeID is the producing node.
	NodeID string `json:"node_id"`
	// InputDigest and ParamsDigest repeat the key inputs for auditability.
	InputDigest  string `json:"input_digest"`
	ParamsDigest string `json:"params_digest"`
	// Status is ok, failed or blocked.
	Status scheduler.Status `json:"status"`
	// Attempts is how many times the node ran in the run that recorded it.
	Attempts int `json:"attempts"`
	// Artifact is the recorded output for an ok node.
	Artifact scheduler.Artifact `json:"artifact"`
	// Error and FaultCode explain a failed node.
	Error     string     `json:"error,omitempty"`
	FaultCode model.Code `json:"fault_code,omitempty"`
	// CompletedAt is the wall-clock completion time in RFC 3339 UTC.
	CompletedAt string `json:"completed_at"`
	// EntryDigest binds the entry's content so tampering is detectable.
	EntryDigest string `json:"entry_digest"`
}

// RunRecord identifies the run that produced a checkpoint, so a resume that
// omits an option can replay the original configuration.
type RunRecord struct {
	Seed     int64  `json:"seed"`
	Fault    string `json:"fault"`
	Manifest string `json:"manifest"`
	Timebase int64  `json:"timebase"`
}

// State is the whole checkpoint document.
type State struct {
	Version int        `json:"version"`
	Run     *RunRecord `json:"run,omitempty"`
	Entries []Entry    `json:"entries"`
	// Digest is the SHA-256 of the canonical body.
	Digest string `json:"digest"`
}

// Store is an atomically written checkpoint file.
type Store struct {
	path string
	now  func() time.Time
}

// New returns a store whose checkpoint file is dir/checkpoint.json.
func New(dir string) *Store { return NewAt(filepath.Join(dir, FileName)) }

// NewAt returns a store backed by exactly the given file path.
func NewAt(path string) *Store {
	return &Store{path: path, now: func() time.Time { return time.Now().UTC() }}
}

// Path returns the checkpoint file path.
func (s *Store) Path() string { return s.path }

// ErrCorrupt marks an unreadable or inconsistent checkpoint.
var ErrCorrupt = errors.New("checkpoint is corrupt")

// Load reads the checkpoint. A missing file returns an empty state with no
// error so a first run needs no special case.
func (s *Store) Load() (*State, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return &State{Version: Version}, nil
	}
	if err != nil {
		return nil, s.fault("cannot read checkpoint: %v", err).WithCause(err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, s.fault("cannot parse checkpoint: %v", err).WithCause(err).WithBytes(0, int64(len(data)))
	}
	if st.Version != Version {
		return nil, model.NewFault(model.CodeCheckpointVersion, model.SeverityError,
			model.Source{Layer: "checkpoint", File: s.path, Offset: -1},
			"checkpoint version %d is not supported (want %d)", st.Version, Version)
	}
	sort.SliceStable(st.Entries, func(i, j int) bool { return st.Entries[i].Key < st.Entries[j].Key })
	body := canonicalBody(st.Run, st.Entries)
	if want := digestOf(body); st.Digest != want {
		return nil, s.fault("checkpoint digest %s does not match content %s", st.Digest, want)
	}
	for i := range st.Entries {
		e := &st.Entries[i]
		if e.EntryDigest != entryDigest(e) {
			return nil, s.fault("entry %d (%s) digest does not match its content", i, e.NodeID)
		}
		if e.Status != scheduler.StatusOK && e.Status != scheduler.StatusFailed && e.Status != scheduler.StatusBlocked {
			return nil, s.fault("entry %d (%s) has invalid status %q", i, e.NodeID, e.Status)
		}
	}
	return &st, nil
}

func (s *Store) fault(format string, args ...any) *model.Fault {
	return model.NewFault(model.CodeCheckpointCorrupt, model.SeverityError,
		model.Source{Layer: "checkpoint", File: s.path, Offset: -1}, format, args...)
}

// Save atomically replaces the checkpoint with entries.
func (s *Store) Save(entries []Entry) error { return s.SaveRun(nil, entries) }

// SaveRun atomically replaces the checkpoint with a run record and entries.
func (s *Store) SaveRun(run *RunRecord, entries []Entry) error {
	st := &State{Version: Version, Run: run, Entries: append([]Entry(nil), entries...)}
	sort.SliceStable(st.Entries, func(i, j int) bool { return st.Entries[i].Key < st.Entries[j].Key })
	for i := range st.Entries {
		if st.Entries[i].CompletedAt == "" {
			st.Entries[i].CompletedAt = s.now().Format(time.RFC3339Nano)
		}
		st.Entries[i].EntryDigest = entryDigest(&st.Entries[i])
	}
	st.Digest = digestOf(canonicalBody(st.Run, st.Entries))
	return s.writeAtomic(st)
}

// Add merges one terminal node into entries, replacing any entry with the same
// key, and returns the new slice.
func Add(entries []Entry, key scheduler.Key, res scheduler.NodeResult, now time.Time) []Entry {
	e := Entry{
		Key: key.Digest(), NodeID: key.NodeID,
		InputDigest: key.InputDigest, ParamsDigest: key.ParamsDigest,
		Status: res.Status, Attempts: res.Attempts, Artifact: res.Artifact,
		Error: res.Error, FaultCode: res.FaultCode,
		CompletedAt: now.UTC().Format(time.RFC3339Nano),
	}
	out := make([]Entry, 0, len(entries)+1)
	for _, old := range entries {
		if old.Key == e.Key {
			continue
		}
		out = append(out, old)
	}
	out = append(out, e)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Completed maps key digests to terminal results for the scheduler.
func Completed(entries []Entry) map[string]scheduler.NodeResult {
	out := make(map[string]scheduler.NodeResult, len(entries))
	for _, e := range entries {
		out[e.Key] = scheduler.NodeResult{
			ID: e.NodeID, Status: e.Status, Attempts: e.Attempts,
			Key: e.Key, Artifact: e.Artifact, Error: e.Error, FaultCode: e.FaultCode,
		}
	}
	return out
}

// writeAtomic writes to a sibling temp file, fsyncs it, then renames over the
// target. A crash leaves either the old checkpoint or the new one.
func (s *Store) writeAtomic(st *State) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return s.fault("cannot create state dir: %v", err).WithCause(err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, ".checkpoint-*.tmp")
	if err != nil {
		return s.fault("cannot create temp checkpoint: %v", err).WithCause(err)
	}
	tmpName := tmp.Name()
	abort := func(err error) error {
		tmp.Close()
		os.Remove(tmpName)
		return s.fault("cannot commit checkpoint: %v", err).WithCause(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return abort(err)
	}
	if err := tmp.Sync(); err != nil {
		return abort(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return abort(err)
	}
	// Best-effort directory sync so the rename survives a power loss.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// canonicalBody renders the run record and entries for digesting with stable
// field order.
func canonicalBody(run *RunRecord, entries []Entry) []byte {
	var buf []byte
	if run != nil {
		buf = append(buf, []byte(fmt.Sprintf("run\x00%d\x00%s\x00%s\x00%d\n",
			run.Seed, run.Fault, run.Manifest, run.Timebase))...)
	}
	return append(buf, canonicalEntries(entries)...)
}

// canonicalEntries renders entries for digesting with stable field order.
func canonicalEntries(entries []Entry) []byte {
	var buf []byte
	appendField := func(s string) {
		buf = append(buf, []byte(s)...)
		buf = append(buf, 0)
	}
	for _, e := range entries {
		appendField(e.Key)
		appendField(e.NodeID)
		appendField(e.InputDigest)
		appendField(e.ParamsDigest)
		appendField(string(e.Status))
		appendField(fmt.Sprintf("%d", e.Attempts))
		appendField(e.Artifact.Digest)
		appendField(fmt.Sprintf("%d", e.Artifact.Size))
		appendField(e.Artifact.Detail)
		appendField(e.Error)
		appendField(string(e.FaultCode))
		appendField(e.CompletedAt)
		buf = append(buf, '\n')
	}
	return buf
}

func digestOf(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func entryDigest(e *Entry) string {
	body := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%s\x00%d\x00%s\x00%s\x00%s\x00%s",
		e.Key, e.NodeID, e.InputDigest, e.ParamsDigest, e.Status, e.Attempts,
		e.Artifact.Digest, e.Artifact.Size, e.Artifact.Detail, e.Error,
		string(e.FaultCode), e.CompletedAt)
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}
