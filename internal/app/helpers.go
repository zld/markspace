package app

import (
	"os"
	"path/filepath"

	"mediatimeline/internal/checkpoint"
	"mediatimeline/internal/clock"
	"mediatimeline/internal/container"
	"mediatimeline/internal/pipeline"
)

// newPipelineForDamage wires a pipeline against an already-scanned event set
// and a random-access source, using an isolated checkpoint directory.
func newPipelineForDamage(cfg pipeline.Config, dir string,
	_ []container.SampleEvent, src *os.File) (*pipeline.Pipeline, error) {
	p, err := pipeline.New(cfg, filepath.Join(dir, "ckpt-damage"),
		filepath.Join(dir, "damage-output.pcm"))
	if err != nil {
		return nil, err
	}
	p.SetSource(src)
	return p, nil
}

// corruptCheckpoint writes a valid checkpoint then deliberately corrupts both
// slots, exercising the unrecoverable-checkpoint path (exit code 3).
func corruptCheckpoint(dir string) (DamageLocation, error) {
	store, err := checkpoint.Open(filepath.Join(dir, "ckpt-corrupt"))
	if err != nil {
		return DamageLocation{}, err
	}
	if err := store.Save(checkpoint.Checkpoint{
		Sequence: 1, Payload: []byte(`{"seed":1}`),
	}); err != nil {
		return DamageLocation{}, err
	}
	if err := store.Corrupt(); err != nil {
		return DamageLocation{}, err
	}
	// Reopening must reject both slots as unrecoverable.
	if _, err := checkpoint.Open(filepath.Join(dir, "ckpt-corrupt")); err != nil {
		if ce, ok := err.(*clock.Error); ok && ce.Code == clock.ErrCheckpointCorrupt {
			return DamageLocation{Offset: ce.FileOffset, TrackID: 0, SampleIndex: ce.SampleIndex}, err
		}
		return DamageLocation{}, err
	}
	return DamageLocation{}, clock.NewError(clock.ErrUnrecoverable, "corrupt checkpoint was accepted")
}
