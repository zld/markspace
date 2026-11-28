// Package recovery persists pipeline checkpoints and drives per-layer resume.
// A checkpoint records the reached stage plus, for every representation, the
// last completed segment and frame sequence. On rerun each layer resumes from
// its own anchor, so one layer never waits on another.
package recovery

import (
	"encoding/json"
	"os"

	"mediapack/internal/manifest"
	"mediapack/internal/model"
)

// LayerCheckpoint is one representation's resume anchor.
type LayerCheckpoint struct {
	Name              string `json:"name"`
	LastSegment       int    `json:"last_completed_segment"`
	LastFrame         int    `json:"last_completed_frame"`
	LastSeq           uint16 `json:"last_completed_seq"`
	CompletedSegments []int  `json:"completed_segments"`
}

// Checkpoint is the on-disk resumable state.
type Checkpoint struct {
	Stage       model.Stage       `json:"stage"`
	Seed        int64             `json:"seed"`
	InputDigest string            `json:"input_digest"`
	Layers      []LayerCheckpoint `json:"layers"`
}

// New builds an empty checkpoint at the created stage.
func New(seed int64, inputDigest string) *Checkpoint {
	return &Checkpoint{
		Stage:       model.StageCreated,
		Seed:        seed,
		InputDigest: inputDigest,
	}
}

// EnsureLayer registers a layer anchor (idempotent).
func (c *Checkpoint) EnsureLayer(name string) {
	if c.layer(name) != nil {
		return
	}
	c.Layers = append(c.Layers, LayerCheckpoint{
		Name: name, LastSegment: -1, LastFrame: -1, LastSeq: 0,
	})
}

// Transition validates and applies a stage move, rejecting illegal jumps.
func (c *Checkpoint) Transition(to model.Stage) error {
	next, err := model.Transition(c.Stage, to)
	if err != nil {
		return err
	}
	c.Stage = next
	return nil
}

// MarkSegmentCompleted advances one layer's anchor after a segment fully
// assembles without gaps. Re-confirming an already completed segment is
// allowed; skipping ahead over a missing segment is rejected.
func (c *Checkpoint) MarkSegmentCompleted(name string, segment, lastFrame int, lastSeq uint16) error {
	lc := c.layer(name)
	if lc == nil {
		return model.NewFieldError(model.KindInvalidInput, "", -1,
			"checkpoint/layer", "unknown layer: "+name)
	}
	if segment <= lc.LastSegment {
		// Idempotent re-confirmation on rerun; anchor must not move backwards.
		return nil
	}
	if segment != lc.LastSegment+1 {
		return model.NewFieldError(model.KindInvalidState, "", -1,
			"checkpoint/segment",
			"cannot skip segments while checkpointing")
	}
	lc.LastSegment = segment
	lc.CompletedSegments = append(lc.CompletedSegments, segment)
	if lastFrame > lc.LastFrame {
		lc.LastFrame = lastFrame
	}
	if lc.LastFrame < 0 || lastSeq > lc.LastSeq {
		lc.LastSeq = lastSeq
	}
	return nil
}

// Layer returns a copy of one layer's anchor.
func (c *Checkpoint) Layer(name string) (LayerCheckpoint, bool) {
	if lc := c.layer(name); lc != nil {
		return *lc, true
	}
	return LayerCheckpoint{}, false
}

func (c *Checkpoint) layer(name string) *LayerCheckpoint {
	for i := range c.Layers {
		if c.Layers[i].Name == name {
			return &c.Layers[i]
		}
	}
	return nil
}

// Save atomically writes the checkpoint inside root.
func (c *Checkpoint) Save(root string) error {
	path, err := manifest.SafePath(root, manifest.FileCheckpoint)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(raw, '\n'))
}

// Load reads a checkpoint; a missing file yields an error the caller can
// distinguish via os.IsNotExist.
func Load(root string) (*Checkpoint, error) {
	path, err := manifest.SafePath(root, manifest.FileCheckpoint)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Checkpoint
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, model.WrapField(model.KindInvalidInput, path, -1,
			manifest.FileCheckpoint, "checkpoint is not valid JSON", err)
	}
	return &c, nil
}

// MatchesInput guards resume against a different input.
func (c *Checkpoint) MatchesInput(seed int64, inputDigest string) bool {
	return c.Seed == seed && c.InputDigest == inputDigest
}
