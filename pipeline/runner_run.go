package pipeline

import "mediacore/media"

// run is the staged execution behind Run. Filled in incrementally:
// stages are ingest -> audio -> per-frame rtp/output, checkpointed at
// GOP boundaries.
func (r *Runner) run() (Result, error) {
	return Result{}, media.Fault(media.CodeInternal, "pipeline.run", -1, "not implemented")
}
