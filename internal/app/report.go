package app

import (
	"mediad/internal/sample"
	"mediad/manifest"
	"mediad/pipeline"
)

// Report is the outcome of one fixture run, including partial/interrupted ones.
type Report struct {
	Kind     sample.Kind
	Runner   *pipeline.Runner
	Results  map[string]pipeline.Result
	Manifest *manifest.Manifest
	Scratch  *scratch
	// Fatal carries the node error when the run did not complete.
	Fatal error
}

// TruncationOffset returns the byte offset of a truncated tail, or -1 when
// the input was not truncated.
func (r *Report) TruncationOffset() int64 {
	if r.Scratch == nil {
		return -1
	}
	return r.Scratch.truncOffset
}

// Stats returns the acceptance metrics, zero-valued when the manifest node
// never ran (e.g. an interrupted or blocked execution).
func (r *Report) Stats() manifest.Stats {
	if r.Manifest != nil {
		return r.Manifest.Stats
	}
	return manifest.Stats{}
}

// Reused reports how many nodes were satisfied from checkpoints.
func (r *Report) Reused() int {
	if r.Runner == nil {
		return 0
	}
	return r.Runner.Reused
}

// Ran reports how many nodes actually executed in this invocation.
func (r *Report) Ran() int {
	if r.Runner == nil {
		return 0
	}
	return r.Runner.Ran
}

// Completed reports whether every DAG node produced a result.
func (r *Report) Completed() bool {
	return r.Fatal == nil && r.Manifest != nil
}

// CheckpointReused is the acceptance metric name: nodes reused on rerun.
func (r *Report) CheckpointReused() int { return r.Reused() }
