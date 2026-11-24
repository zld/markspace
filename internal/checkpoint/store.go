// Package checkpoint persists stage results atomically so a rerun can resume
// from the last trusted stage. Validity is decided by the conjunction of input
// digest, parameter digest and stage version, so a changed source or setting
// invalidates the entry rather than silently reusing stale output.
package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mediad/internal/clock"
)

// Stage names the persisted phases. Ordering is meaningful: resume walks
// forward from the last valid stage.
type Stage string

const (
	StageScan   Stage = "scan"
	StageRepair Stage = "repair"
	StageIndex  Stage = "index"
	StageVerify Stage = "verify"
)

// Order lists stages from earliest to latest.
func Order() []Stage { return []Stage{StageScan, StageRepair, StageIndex, StageVerify} }

// Version is bumped when checkpoint semantics change. It participates in the
// fingerprint so older stores are ignored.
const Version = 1

// Entry is one stage checkpoint on disk.
type Entry struct {
	Version    int             `json:"version"`
	Stage      Stage           `json:"stage"`
	InputSHA   string          `json:"input_sha256"`
	ParamSHA   string          `json:"param_sha256"`
	StageVer   int             `json:"stage_version"`
	OutputSHA  string          `json:"output_sha256"`
	Status     string          `json:"status"` // complete
	Payload    json.RawMessage `json:"payload"`
	Provenance []string        `json:"provenance,omitempty"`
}

// Key identifies the inputs a checkpoint is valid for.
type Key struct {
	InputSHA string
	ParamSHA string
	StageVer int
}

// Match reports whether the entry may be reused.
func (e *Entry) Match(k Key) bool {
	return e != nil && e.Version == Version && e.Status == "complete" &&
		e.InputSHA == k.InputSHA && e.ParamSHA == k.ParamSHA && e.StageVer == k.StageVer
}

// Mismatch explains why an entry was rejected, for logs.
func Mismatch(e *Entry, k Key) string {
	switch {
	case e == nil:
		return "absent"
	case e.Version != Version:
		return fmt.Sprintf("format version %d", e.Version)
	case e.Status != "complete":
		return "incomplete status " + e.Status
	case e.InputSHA != k.InputSHA:
		return "input digest changed"
	case e.ParamSHA != k.ParamSHA:
		return "parameter digest changed"
	case e.StageVer != k.StageVer:
		return fmt.Sprintf("stage version %d", e.StageVer)
	default:
		return "valid"
	}
}

// Store is an on-disk checkpoint directory. Writes are atomic per entry: data
// goes to a temp file in the same directory and is renamed into place.
type Store struct {
	dir string
}

// Open creates or opens a store rooted at dir.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, clock.New(clock.StageCheckpoint, clock.CodeIO, "create checkpoint dir: %v", err).WithCause(err)
	}
	return &Store{dir: dir}, nil
}

// Dir returns the backing directory.
func (s *Store) Dir() string { return s.dir }

func (s *Store) path(stage Stage) string {
	return filepath.Join(s.dir, string(stage)+".json")
}

// Load reads a stage entry. A missing entry returns (nil, nil). Malformed
// JSON returns a structured corruption error so the caller can recompute from
// the nearest trusted stage.
func (s *Store) Load(stage Stage) (*Entry, error) {
	data, err := os.ReadFile(s.path(stage))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, clock.New(clock.StageCheckpoint, clock.CodeIO, "read checkpoint: %v", err).WithCause(err)
	}
	var e Entry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, clock.New(clock.StageCheckpoint, clock.CodeCheckpointCorrupt,
			"checkpoint %s is malformed: %v", stage, err)
	}
	if e.Version != Version {
		return nil, clock.New(clock.StageCheckpoint, clock.CodeCheckpointStale,
			"checkpoint %s has format version %d (want %d)", stage, e.Version, Version)
	}
	if e.Stage != stage {
		return nil, clock.New(clock.StageCheckpoint, clock.CodeCheckpointCorrupt,
			"checkpoint file %s contains stage %s", stage, e.Stage)
	}
	return &e, nil
}

// Save writes an entry atomically and durably.
func (s *Store) Save(e *Entry) error {
	if e == nil {
		return clock.New(clock.StageCheckpoint, clock.CodeBadArgs, "nil checkpoint")
	}
	e.Version = Version
	e.Status = "complete"
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return clock.New(clock.StageCheckpoint, clock.CodeIO, "marshal checkpoint: %v", err).WithCause(err)
	}
	tmp, err := os.CreateTemp(s.dir, ".ckpt-*")
	if err != nil {
		return clock.New(clock.StageCheckpoint, clock.CodeIO, "temp checkpoint: %v", err).WithCause(err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return clock.New(clock.StageCheckpoint, clock.CodeIO, "write checkpoint: %v", err).WithCause(err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return clock.New(clock.StageCheckpoint, clock.CodeIO, "sync checkpoint: %v", err).WithCause(err)
	}
	if err := tmp.Close(); err != nil {
		return clock.New(clock.StageCheckpoint, clock.CodeIO, "close checkpoint: %v", err).WithCause(err)
	}
	if err := os.Rename(name, s.path(e.Stage)); err != nil {
		return clock.New(clock.StageCheckpoint, clock.CodeIO, "rename checkpoint: %v", err).WithCause(err)
	}
	return syncDir(s.dir)
}

// Invalidate removes a stage and every later stage. It is used when a
// checkpoint is corrupt or does not match the current inputs.
func (s *Store) Invalidate(from Stage) error {
	order := Order()
	start := -1
	for i, st := range order {
		if st == from {
			start = i
			break
		}
	}
	if start < 0 {
		return clock.New(clock.StageCheckpoint, clock.CodeBadArgs, "unknown stage %q", from)
	}
	for _, st := range order[start:] {
		if err := os.Remove(s.path(st)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return clock.New(clock.StageCheckpoint, clock.CodeIO, "remove %s: %v", st, err).WithCause(err)
		}
	}
	return nil
}

// ResumePlan reports the last valid stage and the stages that must run. It
// invalidates a corrupt or mismatched entry and every stage after it, so a
// rerun always restarts from trusted state.
func (s *Store) ResumePlan(keys map[Stage]Key) (lastValid Stage, run []Stage, notes []string, err error) {
	order := Order()
	lastIdx := -1
	for i, st := range order {
		key, ok := keys[st]
		if !ok {
			continue
		}
		e, lerr := s.Load(st)
		if lerr != nil {
			// Corrupt or stale: everything from here on is untrusted.
			notes = append(notes, fmt.Sprintf("%s: %v", st, lerr))
			if ierr := s.Invalidate(st); ierr != nil {
				return "", nil, notes, ierr
			}
			break
		}
		if e.Match(key) {
			lastIdx = i
			continue
		}
		notes = append(notes, fmt.Sprintf("%s: %s", st, Mismatch(e, key)))
		if err := s.Invalidate(st); err != nil {
			return "", nil, notes, err
		}
		break
	}
	if lastIdx >= 0 {
		lastValid = order[lastIdx]
	}
	run = append(run, order[lastIdx+1:]...)
	return lastValid, run, notes, nil
}

// Fingerprint hashes the inputs of a stage in a stable order.
func Fingerprint(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		// Length-prefix each part so concatenation is unambiguous.
		fmt.Fprintf(h, "%d:", len(p))
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ParamsFingerprint serializes a parameter struct deterministically. Go's
// encoding/json orders struct fields by declaration and map keys
// alphabetically, so the digest is stable.
func ParamsFingerprint(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", clock.New(clock.StageCheckpoint, clock.CodeBadArgs, "marshal params: %v", err).WithCause(err)
	}
	return Fingerprint(string(data)), nil
}

// List returns stages present on disk, sorted in pipeline order.
func (s *Store) List() ([]Stage, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, clock.New(clock.StageCheckpoint, clock.CodeIO, "list checkpoints: %v", err).WithCause(err)
	}
	present := map[Stage]bool{}
	for _, de := range entries {
		if de.IsDir() {
			continue
		}
		name := strings.TrimSuffix(de.Name(), ".json")
		present[Stage(name)] = true
	}
	var out []Stage
	for _, st := range Order() {
		if present[st] {
			out = append(out, st)
			delete(present, st)
		}
	}
	var extra []Stage
	for st := range present {
		extra = append(extra, st)
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i] < extra[j] })
	return append(out, extra...), nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return clock.New(clock.StageCheckpoint, clock.CodeIO, "open dir: %v", err).WithCause(err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return clock.New(clock.StageCheckpoint, clock.CodeIO, "sync dir: %v", err).WithCause(err)
	}
	return nil
}
