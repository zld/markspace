package fanout

import (
	"mediapack/internal/model"
	"mediapack/internal/render"
	"mediapack/internal/timeline"
)

// Fanout renders and packetizes every bitrate layer against one shared
// renderer/timeline (shared clock fan-in), then runs each layer through the
// same deterministic impairment simulation and reassembles it independently.
// A failure in one layer never blocks the others.
func Fanout(layers []LayerSpec, renderer *render.Renderer, tl *timeline.Timeline, cfg SimConfig) ([]*Representation, error) {
	if len(layers) == 0 {
		return nil, model.NewFieldError(model.KindInvalidInput, "", -1,
			"fanout/layers", "at least one representation layer is required")
	}
	var reps []*Representation
	for _, layer := range layers {
		frames, err := renderer.RenderLayer(layer.LayerConfig(), tl)
		if err != nil {
			return nil, err
		}
		packets, seqToSeg, err := packetize(layer, frames)
		if err != nil {
			return nil, err
		}
		stream := simulate(packets, cfg)
		rec := receive(stream, len(packets), seqToSeg)
		rep, err := buildSegments(layer, frames, rec)
		if err != nil {
			return nil, err
		}
		rep.Sent = len(packets)
		reps = append(reps, rep)
	}
	return reps, nil
}
