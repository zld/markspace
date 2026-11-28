// Package ingest reads containers and packet streams into a unified
// timeline. Every sample keeps its raw DTS/PTS, timebase, per-sample
// duration (VFR is never averaged) and source position.
package ingest

import (
	"releasepipe/internal/clock"
	"releasepipe/internal/errs"
)

// Sample is one access unit with its original timing and source position.
type Sample struct {
	DTS          int64 // decode time in track timebase ticks
	PTS          int64 // presentation time in track timebase ticks
	Duration     int64 // per-sample duration in ticks (VFR preserved)
	Size         uint32
	SourceOffset int64  // byte offset inside the source
	SourceID     string // source file identifier
}

// Track is one timed media track on its own rational timebase.
type Track struct {
	ID         string
	Kind       string // "video" | "audio" | "transition"
	Timebase   clock.Timebase
	Samples    []Sample
	SourcePath string
	Params     map[string]string // codec/container params feeding the digest
}

// PTSNanos returns the presentation time of sample i in nanoseconds.
func (t *Track) PTSNanos(i int) (int64, error) {
	return t.Timebase.ToNanos(t.Samples[i].PTS)
}

// DriftCorrection records the estimated container↔packet clock correction.
type DriftCorrection struct {
	OffsetNanos int64
	SlopePPM    float64
}

// Timeline is the normalized multi-track view shared by index and release.
type Timeline struct {
	Tracks      []*Track
	Corrections map[string]DriftCorrection // per packet-stream kind
}

// ConflictThresholdNanos bounds the tolerated residual between container
// and packet timing after drift correction; beyond it the input conflicts.
const ConflictThresholdNanos = 50_000_000 // 50ms

// BuildTimeline merges container tracks with packet streams, estimating
// clock drift and failing on unresolvable time conflicts.
func BuildTimeline(container []*Track, streams []*PacketStream) (*Timeline, error) {
	tl := &Timeline{Tracks: container, Corrections: map[string]DriftCorrection{}}
	for _, ps := range streams {
		var tr *Track
		for _, t := range container {
			if t.Kind == ps.Kind {
				tr = t
				break
			}
		}
		if tr == nil {
			return nil, errs.New(errs.StageTimeline, errs.CodeTimeConflict, ps.Kind, -1,
				"packet stream has no matching container track")
		}
		n := len(tr.Samples)
		if len(ps.Samples) < n {
			n = len(ps.Samples)
		}
		if n == 0 {
			continue
		}
		tb := clock.Timebase{Num: 1, Den: int64(ps.ClockRate)}
		var est clock.DriftEstimator
		pairs := make([][2]int64, 0, n)
		for i := 0; i < n; i++ {
			local, err := tr.PTSNanos(i)
			if err != nil {
				return nil, errs.Wrap(errs.StageTimeline, errs.CodeInternal, tr.ID, int64(i), err)
			}
			remote, err := tb.ToNanos(ps.Samples[i].PTS)
			if err != nil {
				return nil, errs.Wrap(errs.StageTimeline, errs.CodeInternal, tr.ID, int64(i), err)
			}
			est.Add(local, remote)
			pairs = append(pairs, [2]int64{local, remote})
		}
		off, ppm, ok := est.Correction()
		if ok {
			tl.Corrections[ps.Kind] = DriftCorrection{OffsetNanos: off, SlopePPM: ppm}
		}
		for i, p := range pairs {
			if d := est.Apply(p[1]) - p[0]; d > ConflictThresholdNanos || d < -ConflictThresholdNanos {
				return nil, errs.New(errs.StageTimeline, errs.CodeTimeConflict, tr.ID, int64(i),
					"container/packet time diverge by %d ns at sample %d", d, i)
			}
		}
	}
	return tl, nil
}

// SyncErrorMS reports the end-to-end audio/video misalignment in
// milliseconds: how much the A/V offset drifts from program start to end.
func (tl *Timeline) SyncErrorMS() (float64, error) {
	var v, a *Track
	for _, t := range tl.Tracks {
		switch t.Kind {
		case "video":
			v = t
		case "audio":
			a = t
		}
	}
	if v == nil || a == nil || len(v.Samples) == 0 || len(a.Samples) == 0 {
		return 0, errs.New(errs.StageTimeline, errs.CodeInternal, "", -1,
			"sync check needs non-empty audio and video tracks")
	}
	v0, err := v.PTSNanos(0)
	if err != nil {
		return 0, errs.Wrap(errs.StageTimeline, errs.CodeInternal, v.ID, 0, err)
	}
	a0, err := a.PTSNanos(0)
	if err != nil {
		return 0, errs.Wrap(errs.StageTimeline, errs.CodeInternal, a.ID, 0, err)
	}
	v1, err := v.PTSNanos(len(v.Samples) - 1)
	if err != nil {
		return 0, errs.Wrap(errs.StageTimeline, errs.CodeInternal, v.ID, int64(len(v.Samples)-1), err)
	}
	a1, err := a.PTSNanos(len(a.Samples) - 1)
	if err != nil {
		return 0, errs.Wrap(errs.StageTimeline, errs.CodeInternal, a.ID, int64(len(a.Samples)-1), err)
	}
	drift := (a1 - v1) - (a0 - v0)
	if drift < 0 {
		drift = -drift
	}
	return float64(drift) / 1e6, nil
}
