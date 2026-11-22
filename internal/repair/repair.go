// Package repair corrects track timestamps: clock-drift correction,
// counter wraparound expansion and VFR-preserving monotonic enforcement,
// while annotating every anomaly instead of silently dropping frames.
package repair

import (
	"fmt"

	"mediasync/internal/clock"
	"mediasync/internal/mederr"
)

// AnomalyKind classifies a frame-level repair annotation.
type AnomalyKind string

const (
	AnomalyDrift        AnomalyKind = "drift_corrected"
	AnomalyWrap         AnomalyKind = "timestamp_wrap"
	AnomalyDuplicateTS  AnomalyKind = "duplicate_timestamp"
	AnomalyDiscontinuity AnomalyKind = "discontinuity"
	AnomalyVFRShift     AnomalyKind = "vfr_interval_shift"
	AnomalyTruncated    AnomalyKind = "truncated_segment"
)

// Anomaly is attached to a frame whenever repair altered or flagged it.
type Anomaly struct {
	Kind   AnomalyKind
	Detail string
}

// Frame is one schedulable media frame on a single track.
type Frame struct {
	Track          string
	Representation string
	SegmentNumber  int
	SourceOffset   int64
	IsKeyFrame     bool
	RawTS          uint32 // 32-bit track timestamp, may wrap
	RawTimescale   uint32
	ObservedNS     int64  // pre-repair unified instant
	CorrectedNS    int64  // post-repair unified instant
	Anomalies      []Anomaly
}

// Stats summarises a repair run for metrics and assertions.
type Stats struct {
	Frames           int
	Wraps            int
	DuplicateTS      int
	Discontinuities  int
	VFRIntervalShifts int
	DriftAnchors     int
	Truncated        int
}

// Repairer owns per-track unwrap state and the fitted drift model.
type Repairer struct {
	unwrap map[string]*clock.Unwrapper
	drift  map[string]*clock.DriftModel
}

// NewRepairer builds an empty Repairer.
func NewRepairer() *Repairer {
	return &Repairer{
		unwrap: map[string]*clock.Unwrapper{},
		drift:  map[string]*clock.DriftModel{},
	}
}

// FitDrfit installs a fitted drift model for one track.
func (r *Repairer) FitDrift(track string, anchors []clock.Anchor) error {
	m, err := clock.FitDrift(anchors)
	if err != nil {
		return err
	}
	r.drift[track] = m
	return nil
}

// unwrapTS expands the 32-bit raw timestamp for the frame's track.
func (r *Repairer) unwrapTS(f *Frame) (int64, error) {
	u, ok := r.unwrap[f.Track]
	if !ok {
		var err error
		u, err = clock.NewUnwrapper(32)
		if err != nil {
			return 0, err
		}
		r.unwrap[f.Track] = u
	}
	expanded, wrapped, err := u.Expand(uint64(f.RawTS))
	if err != nil {
		return 0, err
	}
	if wrapped {
		f.Anomalies = append(f.Anomalies, Anomaly{
			Kind:   AnomalyWrap,
			Detail: fmt.Sprintf("32-bit timestamp wrapped at raw=%d", f.RawTS),
		})
	}
	return expanded, nil
}

// RepairFrame expands, corrects and monotonic-enforces one frame.
// truncated marks frames coming from a truncated source segment.
func (r *Repairer) RepairFrame(f *Frame, truncated bool, disc bool) error {
	expanded, err := r.unwrapTS(f)
	if err != nil {
		return err
	}
	tb, err := clock.NewTimeBase(f.RawTimescale)
	if err != nil {
		return err
	}
	rat, err := tb.ToNanos(expanded)
	if err != nil {
		return err
	}
	obs := rat.Ns
	f.ObservedNS = obs
	corrected := obs
	if m, ok := r.drift[f.Track]; ok {
		c, err := m.Correct(obs)
		if err != nil {
			return err
		}
		if c != obs {
			f.Anomalies = append(f.Anomalies, Anomaly{
				Kind:   AnomalyDrift,
				Detail: fmt.Sprintf("%d->%d ns", obs, c),
			})
		}
		corrected = c
	}
	if disc {
		f.Anomalies = append(f.Anomalies, Anomaly{
			Kind:   AnomalyDiscontinuity,
			Detail: "explicit timeline discontinuity",
		})
	}
	if truncated {
		f.Anomalies = append(f.Anomalies, Anomaly{
			Kind:   AnomalyTruncated,
			Detail: "source segment truncated",
		})
	}
	f.CorrectedNS = corrected
	return nil
}

// FinalizeTrack enforces monotonic order on a completed frame slice,
// nudging duplicates, retaining VFR spacing and annotating discontinuities.
func (r *Repairer) FinalizeTrack(frames []*Frame) (Stats, error) {
	var st Stats
	if len(frames) == 0 {
		return st, mederr.New(mederr.CodeInternal, "FinalizeTrack: no frames")
	}
	track := frames[0].Track
	_ = track
	ns := make([]int64, len(frames))
	for i, f := range frames {
		ns[i] = f.CorrectedNS
		st.Frames++
		for _, a := range f.Anomalies {
			switch a.Kind {
			case AnomalyWrap:
				st.Wraps++
			case AnomalyDrift:
				st.DriftAnchors++
			case AnomalyTruncated:
				st.Truncated++
			case AnomalyDiscontinuity:
				st.Discontinuities++
			}
		}
	}
	fixed, dups, discs := clock.EnforceMonotonic(ns)
	var prevGap int64
	for i, f := range frames {
		f.CorrectedNS = fixed[i]
		for _, d := range dups {
			if d == i {
				f.Anomalies = append(f.Anomalies, Anomaly{
					Kind:   AnomalyDuplicateTS,
					Detail: "duplicate timestamp nudged +1ns",
				})
				st.DuplicateTS++
			}
		}
		if i > 0 {
			gap := f.CorrectedNS - frames[i-1].CorrectedNS
			if i > 1 && gap != prevGap && !containsDisc(f) {
				f.Anomalies = append(f.Anomalies, Anomaly{
					Kind:   AnomalyVFRShift,
					Detail: fmt.Sprintf("gap %d->%d ns", prevGap, gap),
				})
				st.VFRIntervalShifts++
			}
			prevGap = gap
		}
	}
	for _, d := range discs {
		frames[d].Anomalies = append(frames[d].Anomalies, Anomaly{
			Kind:   AnomalyDiscontinuity,
			Detail: "backwards step retained as discontinuity",
		})
		st.Discontinuities++
	}
	return st, nil
}

func containsDisc(f *Frame) bool {
	for _, a := range f.Anomalies {
		if a.Kind == AnomalyDiscontinuity {
			return true
		}
	}
	return false
}
