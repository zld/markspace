// Package fanin reconciles fan-out representations onto one shared clock.
// Segment boundaries from every layer are compared in common input ticks; a
// mismatch beyond one input video frame or one audio packet rejects the
// combination before remuxing, so no inconsistent timeline can be trusted.
package fanin

import (
	"fmt"

	"mediacheck/internal/clock"
	"mediacheck/internal/demux"
	merr "mediacheck/internal/errors"
	"mediacheck/internal/fanout"
)

// Tolerance is the maximum allowed boundary difference for edges of one media
// kind, expressed in the ticks of the shared input clock.
type Tolerance struct {
	Limit int64
	Kind  demux.Kind
}

// Tolerances holds the video and audio limits on the shared clock.
type Tolerances struct {
	Video Tolerance
	Audio Tolerance
}

// BoundaryError is one rejected edge, carrying both raw and converted ticks.
type BoundaryError struct {
	LayerID    uint32
	SegmentIdx int
	Edge       string // "start" or "end"
	WantTick   int64  // reference boundary in shared clock ticks
	GotTick    int64  // observed boundary in shared clock ticks
	RawTick    int64  // original timestamp on the layer timescale
	RawScale   uint32
	AbsDelta   int64
	Tolerance  int64
}

// Report is the fan-in verdict.
type Report struct {
	OK            bool
	Errors        []BoundaryError
	StartError    int64 // worst start-edge delta across layers
	EndError      int64 // worst end-edge delta across layers
	ReferenceSegs []fanout.Segment
}

// Verify checks every layer's recorded segments against the reference shared
// clock segments. Layer timestamps are converted from their own timescale
// with exact rational arithmetic. The supplied tolerance applies to the
// media kind of the edges being checked; the report is OK only when every
// edge stays within one frame / one packet.
func Verify(layers []*fanout.LayerState, ref []fanout.Segment, tol Tolerance) (*Report, error) {
	if len(ref) == 0 {
		return nil, merr.New(merr.CodeBoundaryMismatch, "no reference segments for fan-in")
	}
	rep := &Report{OK: true, ReferenceSegs: ref}
	for _, st := range layers {
		segs := st.Segments()
		if len(segs) != len(ref) {
			return nil, merr.New(merr.CodeBoundaryMismatch,
				fmt.Sprintf("layer %d has %d segments, reference has %d",
					st.Layer.ID, len(segs), len(ref))).
				WithState(merr.StateFatal)
		}
		for i, seg := range segs {
			if err := checkEdge(st.Layer.ID, i, "start", ref[i].StartTick,
				seg.StartTick, seg.Timescale, ref[i].Timescale, tol, rep); err != nil {
				return nil, err
			}
			if err := checkEdge(st.Layer.ID, i, "end", ref[i].EndTick,
				seg.EndTick, seg.Timescale, ref[i].Timescale, tol, rep); err != nil {
				return nil, err
			}
		}
	}
	return rep, nil
}

func checkEdge(layerID uint32, idx int, edge string, refTick int64,
	rawTick int64, rawScale, refScale uint32, tol Tolerance, rep *Report) error {

	got, err := clock.Convert(rawTick, rawScale, refScale)
	if err != nil {
		return merr.New(merr.CodeBoundaryMismatch, "timescale conversion failed").Wrap(err)
	}
	delta := abs64(got - refTick)
	if edge == "start" && delta > rep.StartError {
		rep.StartError = delta
	}
	if edge == "end" && delta > rep.EndError {
		rep.EndError = delta
	}
	if delta > tol.Limit {
		rep.OK = false
		rep.Errors = append(rep.Errors, BoundaryError{
			LayerID: layerID, SegmentIdx: idx, Edge: edge,
			WantTick: refTick, GotTick: got,
			RawTick: rawTick, RawScale: rawScale,
			AbsDelta: delta, Tolerance: tol.Limit,
		})
		return merr.New(merr.CodeBoundaryMismatch,
			fmt.Sprintf("layer %d segment %d %s edge off by %d %s ticks (limit %d)",
				layerID, idx, edge, delta, tol.Kind, tol.Limit)).
			AtTick(rawTick, rawScale).WithState(merr.StateFatal)
	}
	return nil
}

// TolerancesFromTracks derives the one-frame / one-packet limits from parsed
// source tracks, converting each onto the shared clock timescale.
func TolerancesFromTracks(tracks []*demux.Track, sharedTimescale uint32) (Tolerances, error) {
	var out Tolerances
	for _, tr := range tracks {
		if len(tr.Samples) == 0 {
			continue
		}
		converted, err := clock.Convert(tr.Samples[0].Duration, tr.Timescale, sharedTimescale)
		if err != nil {
			return out, err
		}
		switch tr.Kind {
		case demux.KindVideo:
			out.Video = Tolerance{Limit: converted, Kind: demux.KindVideo}
		case demux.KindAudio:
			out.Audio = Tolerance{Limit: converted, Kind: demux.KindAudio}
		}
	}
	if out.Video.Limit == 0 || out.Audio.Limit == 0 {
		return out, merr.New(merr.CodeBoundaryMismatch,
			"fan-in needs both a video frame and an audio packet duration")
	}
	return out, nil
}

// ShiftSegment returns a copy of seg with both edges shifted by the given
// number of layer-timescale ticks. Fixture helper for boundary-misalignment
// tests.
func ShiftSegment(seg fanout.Segment, delta int64) fanout.Segment {
	seg.StartTick += delta
	seg.EndTick += delta
	return seg
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
