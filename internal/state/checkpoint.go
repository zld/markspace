// Package state implements resumable, idempotent processing checkpoints.
//
// Every pipeline stage is keyed by the hashes of its inputs, its parameters
// and a stage version: an identical key means the stage can be skipped on a
// re-run. Checkpoints are written atomically (temp file + fsync + rename)
// and validated on load with an HMAC-style envelope, so a torn or corrupted
// file forces recovery from the last fully validated stage rather than
// silently producing wrong results.
package state

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"medtrace/internal/mederr"
)

// StageVersion is bumped whenever a stage's computation changes semantics.
const StageVersion = 1

const (
	dirPerm   = 0o755
	filePerm  = 0o644
	storeName = "checkpoint.json"
	tmpSuffix = ".tmp"
)

// StageResult records one completed stage with full provenance.
type StageResult struct {
	Name       string         `json:"name"`
	Version    int            `json:"version"`
	InputKeys  []string       `json:"input_keys"`
	ParamsHash string         `json:"params_hash"`
	Offsets    []OffsetRange  `json:"offsets"`
	Stats      map[string]any `json:"stats"`
	StartedAt  string         `json:"started_at"`
	EndedAt    string         `json:"ended_at"`
}

// OffsetRange is the inclusive-exclusive byte span an input covered.
type OffsetRange struct {
	Input string `json:"input"`
	Start int64  `json:"start"`
	End   int64  `json:"end"`
}

// envelope is the on-disk checkpoint layout with an integrity tag.
type envelope struct {
	Version int           `json:"version"`
	RunID   string        `json:"run_id"`
	Inputs  []Provenance  `json:"inputs"`
	Stages  []StageResult `json:"stages"`
	Tag     string        `json:"tag"`
}

// Store owns one checkpoint directory.
type Store struct {
	dir string
}

// Open creates or opens a checkpoint directory.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, mederr.New(mederr.CatIO, "create state dir %q", dir).Wrap(err)
	}
	return &Store{dir: dir}, nil
}

// Dir returns the checkpoint directory.
func (s *Store) Dir() string { return s.dir }

func (s *Store) path() string { return filepath.Join(s.dir, storeName) }

// tag computes a keyed digest over every field except the tag itself.
func (e *envelope) computeTag() (string, error) {
	cp := *e
	cp.Tag = ""
	raw, err := json.Marshal(cp)
	if err != nil {
		return "", mederr.New(mederr.CatStateCorrupt, "cannot encode checkpoint").Wrap(err)
	}
	mac := hmac.New(sha256.New, []byte("medtrace-checkpoint-v1"))
	mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// Load reads and validates the checkpoint. A missing checkpoint is a clean
// start (nil, nil). Any structural or tag failure is CatStateCorrupt; the
// caller can then fall back to dropping state and re-running.
func (s *Store) Load() (*envelope, error) {
	raw, err := os.ReadFile(s.path())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, mederr.New(mederr.CatIO, "read checkpoint").Wrap(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, mederr.New(mederr.CatStateCorrupt,
			"checkpoint is not valid JSON; last valid stage cannot be trusted").Wrap(err)
	}
	want, err := env.computeTag()
	if err != nil {
		return nil, err
	}
	if !hmac.Equal([]byte(want), []byte(env.Tag)) {
		return nil, mederr.New(mederr.CatStateCorrupt,
			"checkpoint integrity tag mismatch at %s", s.path())
	}
	if env.Version != StageVersion {
		return nil, mederr.New(mederr.CatStateCorrupt,
			"checkpoint version %d unsupported, want %d", env.Version, StageVersion)
	}
	return &env, nil
}

// Save writes the envelope atomically: temp file, fsync, rename, fsync dir.
func (s *Store) Save(env *envelope) error {
	tag, err := env.computeTag()
	if err != nil {
		return err
	}
	env.Tag = tag
	raw, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return mederr.New(mederr.CatStateCorrupt, "encode checkpoint").Wrap(err)
	}
	tmp := s.path() + tmpSuffix
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, filePerm)
	if err != nil {
		return mederr.New(mederr.CatIO, "create temp checkpoint").Wrap(err)
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return mederr.New(mederr.CatIO, "write checkpoint").Wrap(err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return mederr.New(mederr.CatIO, "fsync checkpoint").Wrap(err)
	}
	if err := f.Close(); err != nil {
		return mederr.New(mederr.CatIO, "close checkpoint").Wrap(err)
	}
	if err := os.Rename(tmp, s.path()); err != nil {
		return mederr.New(mederr.CatIO, "rename checkpoint").Wrap(err)
	}
	return syncDir(s.dir)
}

// Reset discards a corrupt checkpoint so processing restarts from zero; the
// directory itself is preserved.
func (s *Store) Reset() error {
	if err := os.Remove(s.path()); err != nil && !os.IsNotExist(err) {
		return mederr.New(mederr.CatIO, "remove corrupt checkpoint").Wrap(err)
	}
	if err := os.Remove(s.path() + tmpSuffix); err != nil && !os.IsNotExist(err) {
		return mederr.New(mederr.CatIO, "remove stale temp checkpoint").Wrap(err)
	}
	return nil
}

// stageKey builds the idempotency key for one stage invocation.
func stageKey(name string, version int, inputs []Provenance, params map[string]any) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "stage=%s\nversion=%d\n", name, version)
	keys := make([]string, 0, len(inputs))
	for _, in := range inputs {
		keys = append(keys, in.Key())
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "input=%s\n", k)
	}
	if len(params) > 0 {
		raw, err := json.Marshal(params)
		if err != nil {
			return "", mederr.New(mederr.CatInvalidInput, "encode stage params").Wrap(err)
		}
		fmt.Fprintf(h, "params=%s\n", string(raw))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// syncDir flushes directory metadata so a rename is durable after a crash.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return mederr.New(mederr.CatIO, "open state dir for fsync").Wrap(err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return mederr.New(mederr.CatIO, "fsync state dir").Wrap(err)
	}
	return nil
}
