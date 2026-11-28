// Package fanout models a multi-bitrate ladder. Every representation (layer)
// shares one input clock derived from the source track timescales; each layer
// is an independent RTP reorder stream that may lose packets and resume from
// its most recent verifiable keyframe without stalling the other layers.
package fanout

import (
	"fmt"

	"mediacheck/internal/demux"
	merr "mediacheck/internal/errors"
	rtpx "mediacheck/internal/rtp"
)

// Layer is one rung of the bitrate ladder. Since the kernel never transcodes,
// layers differ by RTP packetization and budget rather than re-encoded bytes;
// TargetBitrate still drives the policy's byte-budget choice.
type Layer struct {
	ID            uint32
	SSRC          uint32
	Label         string
	TargetBitrate int // bits per second, informational for the policy
	Window        int // RTP reorder window in packets
}

// Segment marks one fan-out segment on the shared clock.
type Segment struct {
	Index      int
	StartTick  int64
	EndTick    int64
	Timescale  uint32
	IsKeyframe bool
}

// LayerState is the runtime recovery state of one layer.
type LayerState struct {
	Layer       Layer
	Buffer      *rtpx.ReorderBuffer
	recovered   bool
	recoverFrom int64 // tick at which the last keyframe resume happened
	segments    []Segment
}

// Ladder binds layers to one shared input clock.
type Ladder struct {
	// InputTimescale is the clock all segment ticks are expressed on.
	InputTimescale uint32
	layers         map[uint32]*LayerState
	order          []uint32
}

// NewLadder builds a ladder from layer specs. All layers inherit the input
// timescale; distinct SSRCs keep their RTP streams independent.
func NewLadder(inputTimescale uint32, layers []Layer) (*Ladder, error) {
	if inputTimescale == 0 {
		return nil, merr.New(merr.CodeInvalidBox, "fanout input timescale must be nonzero")
	}
	l := &Ladder{InputTimescale: inputTimescale, layers: map[uint32]*LayerState{}}
	seen := map[uint32]bool{}
	for _, spec := range layers {
		if seen[spec.SSRC] {
			return nil, merr.New(merr.CodeInvalidBox,
				fmt.Sprintf("duplicate layer SSRC %d", spec.SSRC))
		}
		seen[spec.SSRC] = true
		window := spec.Window
		if window <= 0 {
			window = 32
		}
		l.layers[spec.ID] = &LayerState{
			Layer:  spec,
			Buffer: rtpx.NewReorderBuffer(window),
		}
		l.order = append(l.order, spec.ID)
	}
	return l, nil
}

// Push feeds one raw RTP datagram to its owning layer, selected by SSRC.
func (l *Ladder) Push(raw []byte) error {
	ssrc := peekSSRC(raw)
	st, ok := l.layersBySSRC()[ssrc]
	if !ok {
		return merr.New(merr.CodeInvalidBox,
			fmt.Sprintf("RTP packet for unknown SSRC %d", ssrc)).
			WithState(merr.StateFatal)
	}
	return st.Buffer.Push(raw)
}

// MarkSegment records a shared-clock segment on every layer so fan-in can
// compare boundaries later.
func (l *Ladder) MarkSegment(seg Segment) {
	for _, id := range l.order {
		l.layers[id].segments = append(l.layers[id].segments, seg)
	}
}

// RecoverLayer resumes a layer after a gap from its latest keyframe segment.
// It returns false when no keyframe is available, meaning the layer cannot
// self-heal. Recovery never touches any other layer.
func (l *Ladder) RecoverLayer(id uint32) (int64, bool) {
	st, ok := l.layers[id]
	if !ok {
		return 0, false
	}
	for i := len(st.segments) - 1; i >= 0; i-- {
		if st.segments[i].IsKeyframe {
			st.recovered = true
			st.recoverFrom = st.segments[i].StartTick
			return st.recoverFrom, true
		}
	}
	return 0, false
}

// Recovered reports whether a layer has performed at least one keyframe resume.
func (l *Ladder) Recovered(id uint32) bool {
	if st, ok := l.layers[id]; ok {
		return st.recovered
	}
	return false
}

// State returns the runtime state of a layer.
func (l *Ladder) State(id uint32) (*LayerState, bool) {
	st, ok := l.layers[id]
	return st, ok
}

// Layers returns layer states in declaration order.
func (l *Ladder) Layers() []*LayerState {
	out := make([]*LayerState, 0, len(l.order))
	for _, id := range l.order {
		out = append(out, l.layers[id])
	}
	return out
}

// Segments returns the shared-clock segments recorded on a layer.
func (s *LayerState) Segments() []Segment { return s.segments }

func (l *Ladder) layersBySSRC() map[uint32]*LayerState {
	out := make(map[uint32]*LayerState, len(l.layers))
	for _, st := range l.layers {
		out[st.Layer.SSRC] = st
	}
	return out
}

// peekSSRC reads bytes 8..11 of an RTP header without fully unmarshalling;
// invalid inputs return zero, which the caller treats as unknown SSRC.
func peekSSRC(raw []byte) uint32 {
	if len(raw) < 12 {
		return 0
	}
	return uint32(raw[8])<<24 | uint32(raw[9])<<16 | uint32(raw[10])<<8 | uint32(raw[11])
}

// DefaultLayers returns the canonical three-rung ladder with deterministic
// SSRCs derived from the seed, suitable for demos and tests.
func DefaultLayers(seed uint32) []Layer {
	base := uint32(0x51510000) + (seed % 1000)
	return []Layer{
		{ID: 0, SSRC: base, Label: "high", TargetBitrate: 2_000_000, Window: 16},
		{ID: 1, SSRC: base + 1, Label: "mid", TargetBitrate: 1_200_000, Window: 16},
		{ID: 2, SSRC: base + 2, Label: "low", TargetBitrate: 600_000, Window: 16},
	}
}

// SegmentsFromTrack derives shared-clock segments from a video track's
// samples; every sync sample starts a keyframe segment.
func SegmentsFromTrack(tr *demux.Track) []Segment {
	segs := make([]Segment, 0, len(tr.Samples))
	start := int64(0)
	for i, s := range tr.Samples {
		if s.Sync && i > 0 {
			segs = append(segs, Segment{
				Index: len(segs), StartTick: start, EndTick: s.DTS,
				Timescale: tr.Timescale, IsKeyframe: true,
			})
			start = s.DTS
		}
	}
	segs = append(segs, Segment{
		Index: len(segs), StartTick: start, EndTick: tr.EndTick(),
		Timescale: tr.Timescale, IsKeyframe: true,
	})
	return segs
}
