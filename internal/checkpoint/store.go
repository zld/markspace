// Package checkpoint persists completed scheduler nodes so an interrupted run
// resumes without repeating acknowledged work. Every write is a temporary file
// followed by an atomic rename, and every entry is validated against a version
// and its own digest.
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
const Version = 1

// FileName is the checkpoint file name inside the state directory.
const FileName = "checkpoint.json"

// Entry is one completed node.
type Entry struct {
	// Key is the idempotency-key digest.
	Key string `json:"key"`
	// NodeID is the producing node.
	NodeID string `json:"node_id"`
	// InputDigest and ParamsDigest repeat the key inputs for auditability.
	InputDigest  string `json:"input_digest"`
	ParamsDigest string `json:"params_digest"`
	// Artifact is the recorded output.
	Artifact scheduler.Artifact `json:"artifact"`
	// CompletedAt is the wall-clock completion time, RFC 3339 UTC.
	CompletedAt string `json:"completed_at"`
	// EntryDigest binds the entry's content so tampering is detectable.
	EntryDigest string `json:"entry_digest"`
}

// State is the whole checkpoint document.
type State struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
	// Digest is the SHA-256 of the canonical body.
	Digest string `json:"digest"`
}

// Store is an atomically written checkpoint file.
type Store struct {
	path string
	// now is injectable for deterministic tests.
	now func() time.Time
}

// New returns a store rooted at dir. The directory is created on first write.
func New(dir string) *Store {
	return &Store{path: filepath.Join(dir, FileName), now: func() time.Time { return time.Now().UTC() }}
}

// Path returns the checkpoint file path.
func (s *Store) Path() string { return s.path }

// ErrCorrupt marks an unreadable or inconsistent checkpoint.
var ErrCorrupt = errors.New("checkpoint is corrupt")

// Load reads the checkpoint. A missing file returns an empty store with no
// error so a first run is not special-cased by callers.
func (s *Store) Load() (*State, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return &State{Version: Version}, nil
	}
	if err != nil {
		return nil, model.NewFault(model.CodeCheckpointCorrupt, model.SeverityError,
			model.Source{Layer: "checkpoint", File: s.path, Offset: -1}, "cannot read checkpoint: %v", err).WithCause(err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, model.NewFault(model.CodeCheckpointCorrupt, model.SeverityError,
			model.Source{Layer: "checkpoint", File: s.path, Offset: -1}, "cannot parse checkpoint: %v", err).
			WithCause(err).WithBytes(0, int64(len(data)))
	}
	if st.Version != Version {
		return nil, model.NewFault(model.CodeCheckpointVersion, model.SeverityError,
			model.Source{Layer: "checkpoint", File: s.path, Offset: -1},
			"checkpoint version %d is not supported (want %d)", st.Version, Version)
	}
	body := canonicalEntries(st.Entries)
	sum := digestOf(body)
	if st.Digest != sum {
		return nil, model.NewFault(model.CodeCheckpointCorrupt, model.SeverityError,
			model.Source{Layer: "checkpoint", File: s.path, Offset: -1},
			"checkpoint digest %s does not match content %s", st.Digest, sum)
	}
	sort.SliceStable(st.Entries, func(i, j int) bool { return st.Entries[i].Key < st.Entries[j].Key })
	for i := range st.Entries {
		e := &st.Entries[i]
		if e.EntryDigest != entryDigest(e) {
			return nil, model.NewFault(model.CodeCheckpointCorrupt, model.SeverityError,
				model.Source{Layer: "checkpoint", File: s.path, Offset: -1},
				"entry %d digest does not match its content", i)
		}
	}
	return &st, nil
}

// Save atomically replaces the checkpoint with entries.
func (s *Store) Save(entries []Entry) error {
	st := &State{Version: Version, Entries: append([]Entry(nil), entries...)}
	sort.SliceStable(st.Entries, func(i, j int) bool { return st.Entries[i].Key < st.Entries[j].Key })
	for i := range st.Entries {
		if st.Entries[i].CompletedAt == "" {
			st.Entries[i].CompletedAt = s.now().Format(time.RFC3339Nano)
		}
		st.Entries[i].EntryDigest = entryDigest(&st.Entries[i])
	}
	st.Digest = digestOf(canonicalEntries(st.Entries))
	return s.writeAtomic(st)
}

// Add merges one completed node into entries, replacing any entry with the same
// key. It returns the new slice; the caller saves it.
func Add(entries []Entry, key scheduler.Key, art scheduler.Artifact, now time.Time) []Entry {
	e := Entry{
		Key: key.Digest(), NodeID: key.NodeID,
		InputDigest: key.InputDigest, ParamsDigest: key.ParamsDigest,
		Artifact: art, CompletedAt: now.UTC().Format(time.RFC3339Nano),
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

// Completed maps key digests to artifacts for the scheduler.
func Completed(entries []Entry) map[string]scheduler.Artifact {
	out := make(map[string]scheduler.Artifact, len(entries))
	for _, e := range entries {
		out[e.Key] = e.Artifact
	}
	return out
}

// writeAtomic writes to a sibling temp file, fsyncs it, then renames over the
// target. A crash leaves either the old checkpoint or the new one, never a
// half-written file.
func (s *Store) writeAtomic(st *State) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return model.NewFault(model.CodeCheckpointCorrupt, model.SeverityError,
			model.Source{Layer: "checkpoint", File: s.path, Offset: -1}, "cannot create state dir: %v", err).WithCause(err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, ".checkpoint-*.tmp")
	if err != nil {
		return model.NewFault(model.CodeCheckpointCorrupt, model.SeverityError,
			model.Source{Layer: "checkpoint", File: s.path, Offset: -1}, "cannot create temp checkpoint: %v", err).WithCause(err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return model.NewFault(model.CodeCheckpointCorrupt, model.SeverityError,
			model.Source{Layer: "checkpoint", File: s.path, Offset: -1}, "cannot write temp checkpoint: %v", err).WithCause(err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return model.NewFault(model.CodeCheckpointCorrupt, model.SeverityError,
			model.Source{Layer: "checkpoint", File: s.path, Offset: -1}, "cannot fsync temp checkpoint: %v", err).WithCause(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		os.Remove(tmpName)
		return model.NewFault(model.CodeCheckpointCorrupt, model.SeverityError,
			model.Source{Layer: "checkpoint", File: s.path, Offset: -1}, "cannot commit checkpoint: %v", err).WithCause(err)
	}
	// Best-effort directory sync so the rename survives a power loss.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// canonicalEntries renders entries for digesting with stable field order.
func canonicalEntries(entries []Entry) []byte {
	var buf []byte
	for _, e := range entries {
		buf = append(buf, []byte(e.Key)...)
		buf = append(buf, 0)
		buf = append(buf, []byte(e.NodeID)...)
		buf = append(buf, 0)
		buf = append(buf, []byte(e.InputDigest)...)
		buf = append(buf, 0)
		buf = append(buf, []byte(e.ParamsDigest)...)
		buf = append(buf, 0)
		buf = append(buf, []byte(e.Artifact.Digest)...)
		buf = append(buf, 0)
		buf = append(buf, []byte(fmt.Sprintf("%d", e.Artifact.Size))...)
		buf = append(buf, 0)
		buf = append(buf, []byte(e.Artifact.Detail)...)
		buf = append(buf, 0)
		buf = append(buf, []byte(e.CompletedAt)...)
		buf = append(buf, '\n')
	}
	return buf
}

func digestOf(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func entryDigest(e *Entry) string {
	body := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%s\x00%s",
		e.Key, e.NodeID, e.InputDigest, e.ParamsDigest,
		e.Artifact.Digest, e.Artifact.Size, e.Artifact.Detail, e.CompletedAt)
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}
