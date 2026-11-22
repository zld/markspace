package clock

import "fmt"

// TrackTimeline is one track's timing on the shared rational base.
// All timestamps are seconds from the shared epoch, already converted from
// the track's own timescale via FromTicks.
type TrackTimeline struct {
	TrackID   uint32
	Timescale uint32
	Kind      string // "video" or "audio"
	Start     Rational // first sample time; non-zero starts are preserved
	Samples   []Rational
}

// DriftEvent records a timestamp regression inside one track.
type DriftEvent struct {
	TrackID  uint32
	Index    int
	Previous Rational
	Current  Rational
}

// Correction is the sync decision for one track. Original timestamps are
// never mutated; the offset is applied by consumers.
type Correction struct {
	TrackID      uint32
	Offset       Rational // estimated track start offset vs. reference track
	NonZeroStart bool
	Regressions  []DriftEvent
}

// SyncConfig bounds what the sync stage tolerates.
type SyncConfig struct {
	MaxSkew Rational // max allowed inter-track (A/V) skew
}

// SkewError reports tracks whose mutual skew exceeds tolerance. The original
// timestamps remain available on the timelines.
type SkewError struct {
	RefID, OtherID uint32
	Skew           Rational
	Tolerance      Rational
}

func (e *SkewError) Error() string {
	return fmt.Sprintf("clock: track %d skew %s s vs track %d exceeds tolerance %s s",
		e.OtherID, e.Skew, e.RefID, e.Tolerance)
}

// DetectRegressions returns indices where sample timestamps move backward.
func DetectRegressions(t TrackTimeline) []DriftEvent {
	var out []DriftEvent
	for i := 1; i < len(t.Samples); i++ {
		if t.Samples[i].Cmp(t.Samples[i-1]) < 0 {
			out = append(out, DriftEvent{
				TrackID:  t.TrackID,
				Index:    i,
				Previous: t.Samples[i-1],
				Current:  t.Samples[i],
			})
		}
	}
	return out
}

// EstimateOffsets computes, for every track, the offset of its first sample
// relative to the reference (first) track, and flags non-zero starts and
// timestamp regressions. Offsets are exact rationals.
func EstimateOffsets(tracks []TrackTimeline) ([]Correction, error) {
	if len(tracks) == 0 {
		return nil, fmt.Errorf("clock: no tracks to synchronize")
	}
	ref := tracks[0].Start
	out := make([]Correction, 0, len(tracks))
	for _, t := range tracks {
		off, err := t.Start.Sub(ref)
		if err != nil {
			return nil, err
		}
		out = append(out, Correction{
			TrackID:      t.TrackID,
			Offset:       off,
			NonZeroStart: t.Start.Cmp(Zero()) != 0,
			Regressions:  DetectRegressions(t),
		})
	}
	return out, nil
}

// CheckAVSkew measures the skew between the first audio and first video
// track starts on the shared rational base, and fails when it exceeds
// cfg.MaxSkew. corr is accepted for symmetry with EstimateOffsets callers;
// the measurement uses raw starts so the original relation is preserved.
func CheckAVSkew(tracks []TrackTimeline, corr []Correction, cfg SyncConfig) (Rational, error) {
	var audio, video *TrackTimeline
	for i := range tracks {
		switch tracks[i].Kind {
		case "audio":
			if audio == nil {
				audio = &tracks[i]
			}
		case "video":
			if video == nil {
				video = &tracks[i]
			}
		}
	}
	if audio == nil || video == nil {
		return Zero(), nil // single-modality input: nothing to compare
	}
	skew, err := audio.Start.Sub(video.Start)
	if err != nil {
		return Zero(), err
	}
	skew = skew.Abs()
	if skew.Cmp(cfg.MaxSkew) > 0 {
		return skew, &SkewError{
			RefID: video.TrackID, OtherID: audio.TrackID,
			Skew: skew, Tolerance: cfg.MaxSkew,
		}
	}
	return skew, nil
}

// DriftRate estimates per-sample drift between two tracks over their common
// sample count: (lastA-firstA) - (lastV-firstV) divided by sample count.
// Exact rational arithmetic; no float accumulation.
func DriftRate(a, b TrackTimeline) (Rational, error) {
	n := len(a.Samples)
	if len(b.Samples) < n {
		n = len(b.Samples)
	}
	if n < 2 {
		return Zero(), nil
	}
	spanA, err := a.Samples[n-1].Sub(a.Samples[0])
	if err != nil {
		return Zero(), err
	}
	spanB, err := b.Samples[n-1].Sub(b.Samples[0])
	if err != nil {
		return Zero(), err
	}
	diff, err := spanA.Sub(spanB)
	if err != nil {
		return Zero(), err
	}
	return diff.Mul(Must(1, int64(n-1)))
}
