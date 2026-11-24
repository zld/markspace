// Package checkpoint persists resumable progress. A checkpoint is only valid
// for the exact input it was produced from: the digest of every source file
// is recorded and re-verified before any resume, so a changed or substituted
// input can never silently continue a previous run.
package checkpoint

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"archive/internal/fault"
	"archive/internal/remux"
)

// Version is the on-disk format version. A mismatch is rejected rather than
// guessed at.
const Version = 1

// InputDigest identifies one source file.
type InputDigest struct {
	Name   string `json:"name"`
	Format string `json:"format"`
	SHA256 string `json:"sha256"`
}

// TrackProgress records what a track has already committed.
type TrackProgress struct {
	Key       string                    `json:"key"`
	Logical   string                    `json:"logical"`
	Done      bool                      `json:"done"`
	Fragments []remux.CommittedFragment `json:"fragments"`
	// OutputOffset is the highest output offset written for the track; it is
	// the anchor a resumed writer continues from.
	OutputOffset uint64 `json:"output_offset"`
}

// State is the persisted checkpoint.
type State struct {
	Version     int             `json:"version"`
	Inputs      []InputDigest   `json:"inputs"`
	InputDigest string          `json:"input_digest"`
	Tracks      []TrackProgress `json:"tracks"`
	// OutputSHA256 is set only after a full, verified run.
	OutputSHA256 string `json:"output_sha256,omitempty"`
	// Completed is true only when every track finished and verification
	// passed.
	Completed bool `json:"completed"`
}

// Store is a filesystem-backed checkpoint.
type Store struct {
	Path string
}

// New creates a store for the given path.
func New(path string) *Store { return &Store{Path: path} }

// Load reads a checkpoint. A missing file is not an error: it means a fresh
// run.
func (s *Store) Load() (*State, error) {
	raw, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, &fault.Error{Code: fault.InvalidArgument, Detail: "checkpoint: unreadable state: " + err.Error()}
	}
	if st.Version != Version {
		return nil, &fault.Error{Code: fault.InvalidArgument, Detail: fmt.Sprintf("checkpoint: version %d is not supported", st.Version)}
	}
	return &st, nil
}

// Save writes the state atomically: a crash during the write must never leave
// a half-written checkpoint that looks valid.
func (s *Store) Save(st *State) error {
	st.Version = Version
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".ckpt-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.Path)
}

// ValidateInputs compares the recorded input digests with the current ones
// and refuses to resume on any difference.
func (s *Store) ValidateInputs(st *State, current []InputDigest) error {
	if st == nil {
		return nil
	}
	if len(st.Inputs) != len(current) {
		return &fault.Error{
			Code:   fault.CheckpointMismatch,
			Detail: fmt.Sprintf("checkpoint: input count changed (%d recorded, %d current)", len(st.Inputs), len(current)),
		}
	}
	rec := append([]InputDigest(nil), st.Inputs...)
	cur := append([]InputDigest(nil), current...)
	sort.Slice(rec, func(i, j int) bool { return rec[i].Name < rec[j].Name })
	sort.Slice(cur, func(i, j int) bool { return cur[i].Name < cur[j].Name })
	for i := range rec {
		if rec[i].Name != cur[i].Name || rec[i].Format != cur[i].Format {
			return &fault.Error{Code: fault.CheckpointMismatch, Detail: fmt.Sprintf("checkpoint: input %d identity changed", i)}
		}
		if rec[i].SHA256 != cur[i].SHA256 {
			return &fault.Error{Code: fault.CheckpointMismatch, Detail: fmt.Sprintf("checkpoint: input %s content changed", rec[i].Name)}
		}
	}
	return nil
}

// Committed returns the fragment map a resumed remux can reuse.
func (st *State) Committed() map[string][]remux.CommittedFragment {
	out := map[string][]remux.CommittedFragment{}
	for _, t := range st.Tracks {
		if len(t.Fragments) > 0 {
			out[t.Key] = append([]remux.CommittedFragment(nil), t.Fragments...)
		}
	}
	return out
}

// ProgressFor returns a track's progress, creating a fresh entry if needed.
func (st *State) ProgressFor(key, logical string) *TrackProgress {
	for i := range st.Tracks {
		if st.Tracks[i].Key == key {
			return &st.Tracks[i]
		}
	}
	st.Tracks = append(st.Tracks, TrackProgress{Key: key, Logical: logical})
	return &st.Tracks[len(st.Tracks)-1]
}

// RecordCommit appends a fragment to a track's progress and persists the
// checkpoint. It is called under the remux writer lock, so the last verified
// checkpoint always reflects every fragment that reached the output.
func (s *Store) RecordCommit(st *State, tr remux.TrackResult, cf remux.CommittedFragment) error {
	p := st.ProgressFor(tr.Key, tr.Logical)
	p.Fragments = append(p.Fragments, cf)
	if end := cf.Offset + cf.Size; end > p.OutputOffset {
		p.OutputOffset = end
	}
	return s.Save(st)
}

// Digest returns a stable digest of an input set, used to key a checkpoint.
func Digest(inputs []InputDigest) string {
	sorted := append([]InputDigest(nil), inputs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	h := sha256.New()
	writeString := func(v string) {
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(v)))
		h.Write(lenBuf[:])
		h.Write([]byte(v))
	}
	for _, in := range sorted {
		writeString(in.Name)
		writeString(in.Format)
		writeString(in.SHA256)
	}
	return hex.EncodeToString(h.Sum(nil))
}
