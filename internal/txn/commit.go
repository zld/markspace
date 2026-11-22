package txn

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrInputChanged is returned when a stage was previously committed with
// different input digests: old stage results must not be reused.
var ErrInputChanged = errors.New("txn: committed stage inputs differ from current inputs")

// InputDigest names one input and its content digest.
type InputDigest struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// CommitPayload is the WAL payload of a commit record.
type CommitPayload struct {
	Inputs  []InputDigest `json:"inputs"`
	Output  string        `json:"output"` // digest of committed output
	Details string        `json:"details,omitempty"`
}

// StageState is the recovered state of one stage.
type StageState struct {
	Committed bool
	Payload   CommitPayload
}

// Coordinator manages atomic stage boundaries inside one work directory.
type Coordinator struct {
	wal    *WAL
	dir    string
	stages map[string]*StageState
	// Rollbacks counts validation failures that triggered a rollback.
	Rollbacks int
	// InputChanges lists stages whose inputs changed vs. the committed WAL.
	InputChanges []string
}

// NewCoordinator opens the WAL in dir, replays it and rebuilds the set of
// committed stages. Recovery resumes from the last valid commit boundary.
func NewCoordinator(dir string) (*Coordinator, error) {
	w, err := OpenWAL(dir)
	if err != nil {
		return nil, err
	}
	c := &Coordinator{wal: w, dir: dir, stages: map[string]*StageState{}}
	recs, err := w.Replay()
	if err != nil {
		w.Close()
		return nil, err
	}
	for _, r := range recs {
		switch r.Kind {
		case KindCommit:
			var p CommitPayload
			if err := jsonUnmarshal(r.Payload, &p); err != nil {
				continue
			}
			c.stages[r.Stage] = &StageState{Committed: true, Payload: p}
		case KindRollback:
			delete(c.stages, r.Stage)
			c.Rollbacks++
		}
	}
	return c, nil
}

// Close closes the underlying WAL.
func (c *Coordinator) Close() error { return c.wal.Close() }

// Dir returns the work directory.
func (c *Coordinator) Dir() string { return c.dir }

// Done reports whether the stage is already committed with identical input
// digests (safe to skip). Committed-with-different-inputs is recorded in
// InputChanges and reported as ErrInputChanged so callers never silently
// reuse stale results.
func (c *Coordinator) Done(stage string, inputs []InputDigest) (bool, error) {
	st, ok := c.stages[stage]
	if !ok || !st.Committed {
		return false, nil
	}
	if digestsEqual(st.Payload.Inputs, inputs) {
		return true, nil
	}
	c.InputChanges = append(c.InputChanges, stage)
	return false, fmt.Errorf("%w: stage %q", ErrInputChanged, stage)
}

// RecordInputs logs the input digests for a stage before work begins.
func (c *Coordinator) RecordInputs(stage string, inputs []InputDigest) error {
	return c.wal.Append(KindInput, stage, inputs)
}

// Begin logs stage start.
func (c *Coordinator) Begin(stage string) error {
	return c.wal.Append(KindBegin, stage, nil)
}

// RecordTemp logs a temporary output digest.
func (c *Coordinator) RecordTemp(stage, digest string) error {
	return c.wal.Append(KindTemp, stage, map[string]string{"digest": digest})
}

// Commit validates, then atomically commits a stage. On validation failure
// every temp artifact inside the work dir is removed and a rollback record
// is appended; committed stages are untouched.
func (c *Coordinator) Commit(stage string, inputs []InputDigest, outputDigest string, temps []string, validate func() error) error {
	if err := validate(); err != nil {
		for _, t := range temps {
			if rmErr := c.removeTemp(t); rmErr != nil {
				return fmt.Errorf("txn: rollback of %s failed: %v (after %v)", t, rmErr, err)
			}
		}
		c.Rollbacks++
		if werr := c.wal.Append(KindRollback, stage, map[string]string{"reason": err.Error()}); werr != nil {
			return werr
		}
		return fmt.Errorf("txn: stage %q rolled back: %w", stage, err)
	}
	p := CommitPayload{Inputs: inputs, Output: outputDigest}
	if err := c.wal.Append(KindCommit, stage, p); err != nil {
		return err
	}
	c.stages[stage] = &StageState{Committed: true, Payload: p}
	return nil
}

// Committed returns the committed payload of a stage, if any.
func (c *Coordinator) Committed(stage string) (CommitPayload, bool) {
	st, ok := c.stages[stage]
	if !ok || !st.Committed {
		return CommitPayload{}, false
	}
	return st.Payload, true
}

// removeTemp deletes a temp artifact, refusing paths outside the work dir.
func (c *Coordinator) removeTemp(path string) error {
	clean := filepath.Clean(path)
	base := filepath.Clean(c.dir) + string(os.PathSeparator)
	if !strings.HasPrefix(clean, base) {
		return fmt.Errorf("txn: path %q escapes work directory", path)
	}
	if err := os.Remove(clean); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func digestsEqual(a, b []InputDigest) bool {
	if len(a) != len(b) {
		return false
	}
	ma := map[string]string{}
	for _, d := range a {
		ma[d.Name] = d.SHA256
	}
	for _, d := range b {
		if ma[d.Name] != d.SHA256 {
			return false
		}
	}
	return true
}
