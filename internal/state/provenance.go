package state

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"medtrace/internal/mederr"
)

// Provenance captures where a stage's bytes came from. The SHA-256 digest
// plus the size makes any input modification invalidate dependent stages.
type Provenance struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Key renders the stable digest key used inside stage idempotency hashes.
func (p Provenance) Key() string {
	return fmt.Sprintf("%s|%d|%s", p.Path, p.Size, p.SHA256)
}

// SummarizeFile hashes a local input file with bounded memory.
func SummarizeFile(path string) (Provenance, error) {
	f, err := os.Open(path)
	if err != nil {
		return Provenance{}, mederr.New(mederr.CatIO, "open input %q", path).Wrap(err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return Provenance{}, mederr.New(mederr.CatIO, "hash input %q", path).Wrap(err)
	}
	return Provenance{
		Path:   path,
		Size:   n,
		SHA256: hex.EncodeToString(h.Sum(nil)),
	}, nil
}

// SummarizeReader hashes an in-memory input (fixtures, tests).
func SummarizeReader(path string, r io.Reader) (Provenance, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return Provenance{}, mederr.New(mederr.CatIO, "hash stream %q", path).Wrap(err)
	}
	return Provenance{Path: path, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// Run is a validated checkpoint session with idempotent stage access.
type Run struct {
	store *Store
	env   *envelope
}

// Begin opens or creates a checkpoint session. A corrupt envelope triggers a
// reset only when allowRecovery is true; otherwise the CatStateCorrupt error
// is returned so the CLI can exit 3 without hiding the failure.
func Begin(dir string, allowRecovery bool) (*Run, error) {
	st, err := Open(dir)
	if err != nil {
		return nil, err
	}
	env, err := st.Load()
	if err != nil {
		if !allowRecovery {
			return nil, err
		}
		if resetErr := st.Reset(); resetErr != nil {
			return nil, resetErr
		}
		env = nil
	}
	if env == nil {
		env = &envelope{Version: StageVersion, RunID: time.Now().UTC().Format("20060102T150405.000000000Z")}
	}
	return &Run{store: st, env: env}, nil
}

// Provenance returns the recorded input summaries.
func (r *Run) Provenance() []Provenance { return append([]Provenance(nil), r.env.Inputs...) }

// SetInputs records the input provenance for this run. If existing stages
// depended on a changed input they are dropped via ShouldRun logic below.
func (r *Run) SetInputs(inputs []Provenance) {
	r.env.Inputs = append([]Provenance(nil), inputs...)
	sort.Slice(r.env.Inputs, func(i, j int) bool { return r.env.Inputs[i].Path < r.env.Inputs[j].Path })
}

// InputHashes returns the sha256 digests in path order, for reporting.
func (r *Run) InputHashes() map[string]string {
	out := map[string]string{}
	for _, in := range r.env.Inputs {
		out[in.Path] = in.SHA256
	}
	return out
}

// ShouldRun reports whether the stage must execute: no stored result, a
// changed idempotency key (input/params/version change), or a missing stage.
func (r *Run) ShouldRun(name string, params map[string]any) (bool, string, error) {
	key, err := stageKey(name, StageVersion, r.env.Inputs, params)
	if err != nil {
		return false, "", err
	}
	for _, st := range r.env.Stages {
		if st.Name != name {
			continue
		}
		want := make([]string, 0, len(r.env.Inputs))
		for _, in := range r.env.Inputs {
			want = append(want, in.Key())
		}
		sort.Strings(want)
		if !equalStrings(st.InputKeys, want) || st.ParamsHash != key || st.Version != StageVersion {
			return true, key, nil
		}
		return false, key, nil
	}
	return true, key, nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Record stores a completed stage result keyed to the current inputs and
// persists the whole envelope atomically.
func (r *Run) Record(name string, params map[string]any, offsets []OffsetRange, stats map[string]any) error {
	key, err := stageKey(name, StageVersion, r.env.Inputs, params)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(r.env.Inputs))
	for _, in := range r.env.Inputs {
		keys = append(keys, in.Key())
	}
	sort.Strings(keys)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res := StageResult{
		Name:       name,
		Version:    StageVersion,
		InputKeys:  keys,
		ParamsHash: key,
		Offsets:    offsets,
		Stats:      stats,
		StartedAt:  now,
		EndedAt:    now,
	}
	// Replace any prior result for the same stage (re-run after invalidation).
	kept := r.env.Stages[:0]
	for _, st := range r.env.Stages {
		if st.Name != name {
			kept = append(kept, st)
		}
	}
	r.env.Stages = append(kept, res)
	sort.SliceStable(r.env.Stages, func(i, j int) bool { return r.env.Stages[i].Name < r.env.Stages[j].Name })
	return r.store.Save(r.env)
}

// Stage returns the stored result for a stage, or false if absent.
func (r *Run) Stage(name string) (StageResult, bool) {
	for _, st := range r.env.Stages {
		if st.Name == name {
			return st, true
		}
	}
	return StageResult{}, false
}

// CompletedStages returns the ordered names of validated stages.
func (r *Run) CompletedStages() []string {
	out := make([]string, 0, len(r.env.Stages))
	for _, st := range r.env.Stages {
		out = append(out, st.Name)
	}
	sort.Strings(out)
	return out
}

// Snapshot returns a serializable view for the report command.
func (r *Run) Snapshot() (inputs []Provenance, stages []StageResult) {
	return r.Provenance(), append([]StageResult(nil), r.env.Stages...)
}
