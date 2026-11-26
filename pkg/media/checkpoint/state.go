// Package checkpoint makes a repair run resumable and idempotent. Only fully
// written, checksum-verified fragments are committed; an interrupted run
// leaves a temp file that the next run discards. On resume, already verified
// items are skipped, and re-running the same input produces byte-identical
// artifacts (same checksums).
package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"cmafrepair/pkg/media/timebase"
)

// StateVersion is bumped when the on-disk checkpoint layout changes.
const StateVersion = "cmaf-checkpoint-v1"

// Item is one committed, verified output artifact.
type Item struct {
	// Key identifies the logical fragment (representation + sequence).
	Key string `json:"key"`
	// OutputPath is the committed file relative to the output directory.
	OutputPath string `json:"output_path"`
	// SHA256 is the digest of the committed bytes.
	SHA256 string `json:"sha256"`
	// Size is the committed file length.
	Size int64 `json:"size"`
	// ResumeMs is the fragment start on the shared clock, for reporting.
	ResumeMs int64 `json:"resume_ms"`
}

// State is the resumable checkpoint persisted next to the output.
type State struct {
	Version   string          `json:"version"`
	RunID     string          `json:"run_id"`
	UpdatedAt string          `json:"updated_at"`
	Items     map[string]Item `json:"items"`
}

// Store loads/saves the checkpoint inside an output directory.
type Store struct {
	dir   string
	path  string
	state State
}

// NewStore binds a checkpoint to an output directory.
func NewStore(outDir string) *Store {
	return &Store{
		dir:   outDir,
		path:  filepath.Join(outDir, ".checkpoint.json"),
		state: State{Version: StateVersion, Items: map[string]Item{}},
	}
}

// Path exposes the checkpoint file location.
func (s *Store) Path() string { return s.path }

// Load reads an existing checkpoint. A missing file is a clean start, not an
// error; a malformed file is rejected so stale state cannot silently steer a
// resume.
func (s *Store) Load() error {
	b, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return timebase.NewError(timebase.CodeIO, timebase.PhaseCheckpoint,
			"read checkpoint: %v", err)
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return timebase.NewError(timebase.CodeInputInvalid, timebase.PhaseCheckpoint,
			"malformed checkpoint: %v", err)
	}
	if st.Version != StateVersion {
		return timebase.NewError(timebase.CodeInputInvalid, timebase.PhaseCheckpoint,
			"checkpoint version %q want %q", st.Version, StateVersion)
	}
	if st.Items == nil {
		st.Items = map[string]Item{}
	}
	s.state = st
	return nil
}

// IsVerified reports whether key was already committed with the same digest.
// The on-disk file is re-hashed so a replaced artifact invalidates the entry.
func (s *Store) IsVerified(key string) bool {
	item, ok := s.state.Items[key]
	if !ok {
		return false
	}
	full := filepath.Join(s.dir, item.OutputPath)
	sum, err := hashFile(full)
	if err != nil || sum != item.SHA256 {
		// File missing, changed or replaced: drop the stale entry.
		delete(s.state.Items, key)
		return false
	}
	return true
}

// Item returns the recorded item for a key.
func (s *Store) Item(key string) (Item, bool) {
	it, ok := s.state.Items[key]
	return it, ok
}

// Commit atomically records a verified item. The caller passes the already
// renamed output path (relative) and its expected digest; the digest is
// re-checked before recording so a corrupt file is never committed.
func (s *Store) Commit(key, relPath string, resumeMs int64) (Item, error) {
	full := filepath.Join(s.dir, relPath)
	sum, size, err := hashFileSize(full)
	if err != nil {
		return Item{}, err
	}
	item := Item{
		Key: key, OutputPath: relPath, SHA256: sum,
		Size: size, ResumeMs: resumeMs,
	}
	s.state.Items[key] = item
	s.state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.persist(); err != nil {
		return Item{}, err
	}
	return item, nil
}

// Count returns the number of committed items.
func (s *Store) Count() int { return len(s.state.Items) }

// SortedItems returns committed items ordered by key, for deterministic
// reports and bundle hashing.
func (s *Store) SortedItems() []Item {
	out := make([]Item, 0, len(s.state.Items))
	for _, it := range s.state.Items {
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// BundleHash hashes the ordered per-item (key, sha256) pairs, giving one
// digest that changes iff any committed artifact changes. Repeated runs over
// identical inputs therefore yield an identical bundle hash.
func (s *Store) BundleHash() string {
	items := s.SortedItems()
	h := sha256.New()
	for _, it := range items {
		io.WriteString(h, it.Key)
		io.WriteString(h, ":")
		io.WriteString(h, it.SHA256)
		io.WriteString(h, "\n")
	}
	return hex.EncodeToString(h.Sum(nil))
}

// CleanupTemp removes leftover temp files from an interrupted run. Temp files
// are never promoted, so deleting them is always safe.
func (s *Store) CleanupTemp() ([]string, error) {
	var removed []string
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return removed, nil
		}
		return nil, timebase.NewError(timebase.CodeIO, timebase.PhaseCheckpoint,
			"read output dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if len(name) >= 4 && name[:4] == ".tmp" {
			p := filepath.Join(s.dir, name)
			if err := os.Remove(p); err != nil {
				return removed, timebase.NewError(timebase.CodeIO, timebase.PhaseCheckpoint,
					"remove temp %s: %v", name, err)
			}
			removed = append(removed, name)
		}
	}
	return removed, nil
}

// persist writes the checkpoint through a temp file and atomic rename.
func (s *Store) persist() error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return timebase.NewError(timebase.CodeIO, timebase.PhaseCheckpoint, "mkdir: %v", err)
	}
	b, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return timebase.NewError(timebase.CodeInputInvalid, timebase.PhaseCheckpoint,
			"marshal: %v", err)
	}
	tmp, err := os.CreateTemp(s.dir, ".tmp-checkpoint-*")
	if err != nil {
		return timebase.NewError(timebase.CodeIO, timebase.PhaseCheckpoint, "temp: %v", err)
	}
	name := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(name)
		}
	}()
	if _, err := tmp.Write(b); err != nil {
		return timebase.NewError(timebase.CodeIO, timebase.PhaseCheckpoint, "write: %v", err)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, s.path); err != nil {
		return timebase.NewError(timebase.CodeIO, timebase.PhaseCheckpoint, "rename: %v", err)
	}
	committed = true
	return nil
}

func hashFile(p string) (string, error) {
	sum, _, err := hashFileSize(p)
	return sum, err
}

func hashFileSize(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, timebase.NewError(timebase.CodeIO, timebase.PhaseCheckpoint,
			"open %s: %v", p, err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, timebase.NewError(timebase.CodeIO, timebase.PhaseCheckpoint,
			"hash %s: %v", p, err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
