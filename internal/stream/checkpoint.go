// Package stream executes the conversion from a repaired, indexed timeline.
// It is resumable: each fully processed sample is checkpointed, an injected
// interruption leaves a reusable streaming state, and output writes are
// de-duplicated by stable input key so re-runs never double-write.
package stream

import (
	"encoding/json"
	"os"
	"path/filepath"

	"avpipe/internal/model"
)

// Pipeline states mirror model; file names are the on-disk state artifacts.
const (
	StateIndexed   = model.StateIndexed
	StatePlanned   = model.StatePlanned
	StateStreaming = model.StateStreaming
	StateCommitted = model.StateCommitted

	StateFile   = "state.json"
	JournalFile = "journal.dat"
)

var legalTransitions = map[string]map[string]bool{
	StateIndexed:   {StatePlanned: true},
	StatePlanned:   {StateStreaming: true},
	StateStreaming: {StateStreaming: true, StateCommitted: true},
	StateCommitted: {},
}

// Checkpoint is the durable, resumable processing state.
type Checkpoint struct {
	State  string `json:"state"`
	Digest string `json:"digest"`
	// LastSample is the global ordinal of the last fully processed sample.
	// Resume continues at LastSample+1.
	LastSample  int               `json:"last_sample"`
	WrittenKeys map[string]string `json:"written_keys"` // stable key -> hash
	StopAfter   int               `json:"stop_after"`
	Interrupted bool              `json:"interrupted"`
}

// NewCheckpoint starts the state machine at indexed.
func NewCheckpoint(digest string) *Checkpoint {
	return &Checkpoint{
		State: StateIndexed, Digest: digest,
		LastSample: -1, WrittenKeys: map[string]string{}, StopAfter: -1,
	}
}

// Move applies a state transition, rejecting illegal ones.
func (c *Checkpoint) Move(to string) error {
	if !legalTransitions[c.State][to] {
		return model.NewError(model.CodeState, false,
			"illegal transition %s -> %s", c.State, to)
	}
	c.State = to
	return nil
}

// Save writes the checkpoint atomically so a crash never corrupts state.
func (c *Checkpoint) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return model.NewError(model.CodeIO, false, "mkdir state: %v", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return model.NewError(model.CodeIO, false, "marshal state: %v", err)
	}
	path := filepath.Join(dir, StateFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return model.NewError(model.CodeIO, false, "write state: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return model.NewError(model.CodeIO, false, "rename state: %v", err)
	}
	return nil
}

// LoadCheckpoint reads state and guards against input digest changes.
func LoadCheckpoint(dir, digest string) (*Checkpoint, error) {
	data, err := os.ReadFile(filepath.Join(dir, StateFile))
	if err != nil {
		return nil, model.NewError(model.CodeIO, false, "read state: %v", err)
	}
	var c Checkpoint
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, model.NewError(model.CodeState, false, "parse state: %v", err)
	}
	if c.WrittenKeys == nil {
		c.WrittenKeys = map[string]string{}
	}
	if c.Digest != digest {
		return nil, model.NewError(model.CodeDigestMismatch, true,
			"input changed since checkpoint: state=%s input=%s", c.Digest, digest)
	}
	return &c, nil
}
