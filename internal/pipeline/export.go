package pipeline

import (
	"mediapack/internal/fanout"
	"mediapack/internal/manifest"
	"mediapack/internal/model"
	"mediapack/internal/render"
	"mediapack/internal/timeline"
)

// layerState collects one layer's processed output for manifest assembly.
type layerState struct {
	result *fanout.LayerResult
	// recordFrames are the frames durably written (only anchored segments on
	// an interrupted run; every frame on a completed run).
	recordFrames []manifest.FrameEntry
}

// exportLayers runs every representation through fanout and persists the frame
// records of every anchored segment. Layers resume independently.
func (r *run) exportLayers(renderer *render.Renderer, tl *timeline.Timeline) (map[string]*layerState, error) {
	if err := r.cp.Transition(model.StageExported); err != nil {
		return nil, err
	}
	states := map[string]*layerState{}
	totalSegments := segmentCount()
	lastSegment := uint32(totalSegments - 1)

	for _, layer := range r.layers {
		cfg := fanout.DemoSimConfig(uint64(r.opts.Seed), lastSegment)
		maxSeg := 0
		interrupting := r.opts.InterruptAfter > 0 && !r.opts.Resume
		if interrupting {
			maxSeg = r.opts.InterruptAfter
		}
		// A normal run and a resume both recover gaps by retransmitting the
		// original missing packets. Only a deliberately interrupted first run
		// leaves gaps open so the checkpoint demonstrates resumable state.
		retransmit := !interrupting
		res, err := fanout.ProcessLayer(layer, renderer, tl, cfg, maxSeg, retransmit)
		if err != nil {
			return nil, err
		}
		st := &layerState{result: res}

		anchor, _ := r.cp.Layer(layer.Name)
		// On a fresh run seed the checkpoint anchor from this processing; on
		// resume move it forward through every now-complete segment.
		for si := 0; si < len(res.Rep.Segments); si++ {
			seg := res.Rep.Segments[si]
			if !seg.Complete {
				continue
			}
			var lastSeq uint16
			if len(seg.Seqs) > 0 {
				lastSeq = seg.Seqs[len(seg.Seqs)-1]
			}
			if si > anchor.LastSegment {
				if err := r.cp.MarkSegmentCompleted(layer.Name, si, seg.FrameEnd, lastSeq); err != nil {
					return nil, err
				}
			}
			anchor, _ = r.cp.Layer(layer.Name)
		}

		// Persist frame records only up to the anchored frame.
		limit := anchor.LastFrame
		for i, f := range res.Frames {
			if i > limit {
				break
			}
			st.recordFrames = append(st.recordFrames, manifest.FrameEntry{
				Index: f.Index, Tick: f.Tick, SourceTrack: f.SourceTrack,
				SourceSample: f.SourceSample, Width: f.Width, Height: f.Height,
				PixelFormat: f.PixelFormat, InputDigest: f.InputDigest, SHA256: f.Hash,
			})
		}
		if err := manifest.WriteFrameRecords(r.opts.OutDir, layer.Name, st.recordFrames); err != nil {
			return nil, err
		}
		states[layer.Name] = st
		r.emit(Event{Stage: model.StageExported,
			Message: layerSummaryLine(layer.Name, res, anchor.LastSegment)})
	}
	return states, nil
}

// segmentCount mirrors the demo frames-per-segment geometry.
func segmentCount() int {
	const frames, perSeg = 12, 4
	return (frames-1)/perSeg + 1
}

func layerSummaryLine(name string, res *fanout.LayerResult, anchored int) string {
	return "layer " + name +
		" segments=" + itoa(len(res.Rep.Segments)) +
		" anchored_segment=" + itoa(anchored) +
		" open_gaps=" + itoa(len(res.Rep.Gaps)) +
		" recovered=" + itoa(res.RecoveredGaps)
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
