package pipeline

import (
	"mediapack/internal/audio"
	"mediapack/internal/fanout"
	"mediapack/internal/manifest"
	"mediapack/internal/model"
	"mediapack/internal/timeline"
)

// finalize builds, writes and independently verifies the sealed manifest.
func (r *run) finalize(tl *timeline.Timeline, states map[string]*layerState,
	stats []audio.TrackStats) (*Result, error) {
	var reps []*fanout.Representation
	frameByLayer := map[string][]manifest.FrameEntry{}
	totalFrames := 0
	for _, layer := range r.layers {
		st := states[layer.Name]
		if !st.result.Rep.Complete() {
			return nil, model.NewFieldError(model.KindGap, r.opts.OutDir, -1,
				"fanout/"+layer.Name,
				"representation still has open gaps; cannot finalize")
		}
		reps = append(reps, st.result.Rep)
		frameByLayer[layer.Name] = st.recordFrames
		totalFrames += len(st.recordFrames)
	}

	if err := r.cp.Transition(model.StageVerified); err != nil {
		return nil, err
	}
	m := manifest.Build(r.opts.Seed, model.StageVerified, r.cp.InputDigest,
		tl.DurationTicks, tl.MaxAlignment, reps, frameByLayer, stats)
	hash, err := manifest.Write(r.opts.OutDir, m)
	if err != nil {
		return nil, err
	}
	if err := r.cp.Save(r.opts.OutDir); err != nil {
		return nil, err
	}

	// Independent verification from disk, not from in-memory state.
	report, err := manifest.Verify(r.opts.OutDir)
	if err != nil || !report.OK {
		if err != nil {
			return nil, err
		}
		return nil, model.NewFieldError(model.KindChecksum, r.opts.OutDir, -1,
			"verify", "post-write verification found differences")
	}
	r.emit(Event{Stage: model.StageVerified, Message: "manifest verified ok"})
	return r.completedResult(tl, states, stats, hash, totalFrames), nil
}

// interruptedResult summarizes a deliberately halted run. Checkpoint and frame
// records for the anchored segments are already on disk.
func (r *run) interruptedResult(tl *timeline.Timeline, states map[string]*layerState,
	stats []audio.TrackStats) *Result {
	r.emit(Event{Stage: model.StageExported,
		Message: "interrupted after " + itoa(r.opts.InterruptAfter) + " segments per layer"})
	res := &Result{
		Stage:         model.StageExported,
		Interrupted:   true,
		InputDigest:   r.cp.InputDigest,
		DurationTicks: tl.DurationTicks,
		MaxAlignTicks: tl.MaxAlignment.MaxErrorTicks,
		Audio:         stats,
	}
	res.Layers = r.summaries(states)
	return res
}

func (r *run) completedResult(tl *timeline.Timeline, states map[string]*layerState,
	stats []audio.TrackStats, hash string, totalFrames int) *Result {
	res := &Result{
		Stage:         model.StageVerified,
		InputDigest:   r.cp.InputDigest,
		ManifestHash:  hash,
		DurationTicks: tl.DurationTicks,
		MaxAlignTicks: tl.MaxAlignment.MaxErrorTicks,
		FrameCount:    totalFrames,
		Audio:         stats,
	}
	res.Layers = r.summaries(states)
	return res
}

func (r *run) summaries(states map[string]*layerState) []LayerSummary {
	out := make([]LayerSummary, 0, len(r.layers))
	for _, layer := range r.layers {
		st := states[layer.Name]
		anchor, _ := r.cp.Layer(layer.Name)
		out = append(out, LayerSummary{
			Name:          layer.Name,
			Frames:        len(st.recordFrames),
			LastSegment:   anchor.LastSegment,
			LastFrame:     anchor.LastFrame,
			RecoveredGaps: st.result.RecoveredGaps,
			OpenGaps:      len(st.result.Rep.Gaps),
			Complete:      st.result.Rep.Complete(),
		})
	}
	return out
}
