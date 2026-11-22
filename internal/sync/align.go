// Package sync aligns normalized tracks on the unified media line and checks
// layout declarations across fragments. Alignment is observational: missing
// transport state is propagated as unresolved regions, never interpolated.
package sync

import (
	"errors"
	"fmt"
	"sort"

	"mediapipe/internal/clock"
)

var (
	// ErrNonMonotonic reports sample times that move backwards.
	ErrNonMonotonic = errors.New("sync: non-monotonic media time")
	// ErrNoReference reports an alignment without a usable reference track.
	ErrNoReference = errors.New("sync: no reference samples")
	// ErrMissingState reports an alignment attempted across a transport gap.
	ErrMissingState = errors.New("sync: unresolved transport gap")
	// ErrUnknownTrack reports alignment for an undeclared track id.
	ErrUnknownTrack = errors.New("sync: unknown track")
)

// MissingRange mirrors the transport missing state on the unified timeline.
type MissingRange struct {
	TrackID    uint32
	StartTicks int64
	EndTicks   int64
	Packets    int
	Source     string // e.g. "rtp:lost", kept for traceability
}

// RawSample is one container sample before timebase conversion.
type RawSample struct {
	DTS      uint64 // track ticks
	CTS      int64  // track ticks
	Duration uint32 // track ticks
	Sync     bool
	Digest   [32]byte
}

// SampleTime is a sample on the unified 90 kHz line.
type SampleTime struct {
	DTS      int64
	CTS      int64
	Duration int64
	Sync     bool
	Digest   [32]byte
}

// TrackView is a track's normalized timing plus its transport history.
type TrackView struct {
	TrackID   uint32
	Kind      string // "video", "audio", "subtitle", ...
	Timescale uint32
	Samples   []SampleTime
	Missing   []MissingRange
}

// Diagnostic is a locatable finding. It always names the track and field; it
// never proposes an automatic rewrite of source values.
type Diagnostic struct {
	Code     string
	TrackID  uint32
	Segment  uint32
	Field    string
	Message  string
	Severity string // "error" or "warning"
}

func (d Diagnostic) String() string {
	return fmt.Sprintf("%s:%s track=%d segment=%d field=%s: %s", d.Severity, d.Code, d.TrackID, d.Segment, d.Field, d.Message)
}

// ConvertSamples maps raw container samples onto the unified line. Each
// conversion contributes at most half a tick of rounding error; the caller can
// bound the total with RoundingBound.
func ConvertSamples(timescale uint32, raw []RawSample) ([]SampleTime, int64, error) {
	if timescale == 0 {
		return nil, 0, fmt.Errorf("sync: convert: %w", clock.ErrZeroTimescale)
	}
	out := make([]SampleTime, 0, len(raw))
	var uncertainty int64
	for i, s := range raw {
		dts, err := clock.Convert(int64(s.DTS), timescale, clock.UnifiedTimescale)
		if err != nil {
			return nil, 0, fmt.Errorf("sync: sample %d dts: %w", i, err)
		}
		cts, err := clock.Convert(s.CTS, timescale, clock.UnifiedTimescale)
		if err != nil {
			return nil, 0, fmt.Errorf("sync: sample %d cts: %w", i, err)
		}
		dur, err := clock.Convert(int64(s.Duration), timescale, clock.UnifiedTimescale)
		if err != nil {
			return nil, 0, fmt.Errorf("sync: sample %d duration: %w", i, err)
		}
		out = append(out, SampleTime{DTS: dts, CTS: cts, Duration: dur, Sync: s.Sync, Digest: s.Digest})
	}
	// Each sample contributes one half-tick rounding bound for DTS and one for
	// CTS; the total is reported so callers can widen tolerance honestly.
	uncertainty, err := RoundingBound(int64(len(raw) * 2))
	if err != nil {
		return nil, 0, err
	}
	return out, uncertainty, nil
}

// RoundingBound returns the worst-case accumulated error, in ticks, of count
// half-tick roundings. It fails rather than wrapping on absurd counts.
func RoundingBound(count int64) (int64, error) {
	if count < 0 {
		return 0, fmt.Errorf("sync: negative rounding count %d", count)
	}
	if count > 1<<40 {
		return 0, fmt.Errorf("sync: rounding count %d out of range", count)
	}
	return (count + 1) / 2, nil // ceil(count/2) half-ticks
}

// InGap reports whether a unified interval intersects any missing range.
func (v TrackView) InGap(start, end int64) (MissingRange, bool) {
	for _, m := range v.Missing {
		if start <= m.EndTicks && m.StartTicks <= end {
			return m, true
		}
	}
	return MissingRange{}, false
}

// AlignConfig bounds alignment acceptance.
type AlignConfig struct {
	// ToleranceTicks is the maximum acceptable offset between the two tracks.
	ToleranceTicks int64
	// BaseUncertainty is the caller's known error budget in ticks.
	BaseUncertainty int64
}

func (c AlignConfig) validate() error {
	if c.ToleranceTicks < 0 || c.ToleranceTicks > 90*90000 {
		return fmt.Errorf("sync: tolerance %d out of range", c.ToleranceTicks)
	}
	if c.BaseUncertainty < 0 || c.BaseUncertainty > 90000 {
		return fmt.Errorf("sync: base uncertainty %d out of range", c.BaseUncertainty)
	}
	return nil
}

// Alignment is the result of aligning one track to a reference.
type Alignment struct {
	TrackID     uint32
	RefTrackID  uint32
	Offset      int64 // target - reference at the first common sample
	Uncertainty int64 // propagated bound, including rounding
	Matched     int
	Outliers    int
	Status      string // "aligned", "outlier" or "unresolved"
	Diagnostics []Diagnostic
}

// Align aligns target to ref by nearest-neighbour comparison of composition
// times. A target sample that falls in a transport gap makes the alignment
// unresolved: no offset is fabricated for it.
func Align(ref, target TrackView, cfg AlignConfig) (Alignment, error) {
	if err := cfg.validate(); err != nil {
		return Alignment{}, err
	}
	a := Alignment{TrackID: target.TrackID, RefTrackID: ref.TrackID, Status: "aligned"}
	if len(ref.Samples) == 0 {
		return a, fmt.Errorf("%w: track %d", ErrNoReference, ref.TrackID)
	}
	if len(target.Samples) == 0 {
		return a, fmt.Errorf("%w: track %d has no samples", ErrNoReference, target.TrackID)
	}
	if err := ensureMonotonic(ref); err != nil {
		return a, err
	}
	if err := ensureMonotonic(target); err != nil {
		return a, err
	}
	if m, ok := target.InGap(target.Samples[0].CTS, target.Samples[len(target.Samples)-1].CTS); ok {
		a.Status = "unresolved"
		a.Diagnostics = append(a.Diagnostics, Diagnostic{
			Code: "transport_gap", TrackID: target.TrackID, Field: "cts",
			Severity: "error",
			Message:  fmt.Sprintf("missing state %s covers [%d..%d] packets=%d", m.Source, m.StartTicks, m.EndTicks, m.Packets),
		})
		return a, fmt.Errorf("%w: track %d [%d..%d]", ErrMissingState, target.TrackID, m.StartTicks, m.EndTicks)
	}
	refTimes := make([]int64, len(ref.Samples))
	for i, s := range ref.Samples {
		refTimes[i] = s.CTS
	}
	var deltas []int64
	a.Matched = len(target.Samples)
	for i, ts := range target.Samples {
		if m, ok := target.InGap(ts.CTS, ts.CTS); ok {
			a.Status = "unresolved"
			a.Diagnostics = append(a.Diagnostics, Diagnostic{
				Code: "transport_gap", TrackID: target.TrackID, Field: "cts", Severity: "error",
				Message: fmt.Sprintf("sample %d in gap %s [%d..%d]", i, m.Source, m.StartTicks, m.EndTicks),
			})
			a.Matched = i
			return a, fmt.Errorf("%w: track %d sample %d", ErrMissingState, target.TrackID, i)
		}
		nearest := nearestIndex(refTimes, ts.CTS)
		deltas = append(deltas, ts.CTS-ref.Samples[nearest].CTS)
	}
	offset := median(deltas)
	a.Offset = offset
	rounding, err := RoundingBound(int64(len(target.Samples) * 2))
	if err != nil {
		return a, err
	}
	a.Uncertainty = cfg.BaseUncertainty + rounding
	for i, d := range deltas {
		if abs64(d-offset) > cfg.ToleranceTicks+a.Uncertainty {
			a.Outliers++
			a.Diagnostics = append(a.Diagnostics, Diagnostic{
				Code: "align_outlier", TrackID: target.TrackID, Field: "cts", Severity: "warning",
				Message: fmt.Sprintf("sample %d delta %d outside tolerance %d +/- %d", i, d, cfg.ToleranceTicks, a.Uncertainty),
			})
		}
	}
	if a.Outliers > 0 {
		a.Status = "outlier"
	}
	return a, nil
}

func ensureMonotonic(v TrackView) error {
	for i := 1; i < len(v.Samples); i++ {
		if v.Samples[i].CTS < v.Samples[i-1].CTS {
			return fmt.Errorf("%w: track %d sample %d cts %d < %d", ErrNonMonotonic,
				v.TrackID, i, v.Samples[i].CTS, v.Samples[i-1].CTS)
		}
	}
	return nil
}

func nearestIndex(sorted []int64, v int64) int {
	i := sort.Search(len(sorted), func(i int) bool { return sorted[i] >= v })
	if i == 0 {
		return 0
	}
	if i == len(sorted) {
		return len(sorted) - 1
	}
	if v-sorted[i-1] <= sorted[i]-v {
		return i - 1
	}
	return i
}

func median(in []int64) int64 {
	if len(in) == 0 {
		return 0
	}
	cp := append([]int64(nil), in...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	return cp[len(cp)/2]
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// KeyframeHitRate measures how many reference keyframe times find a keyframe in
// the target index at exactly the same unified time.
func KeyframeHitRate(ref, target []int64) float64 {
	if len(ref) == 0 {
		return 1
	}
	have := make(map[int64]struct{}, len(target))
	for _, t := range target {
		have[t] = struct{}{}
	}
	hits := 0
	for _, t := range ref {
		if _, ok := have[t]; ok {
			hits++
		}
	}
	return float64(hits) / float64(len(ref))
}

// PropagateMissing merges transport missing ranges into a track view after the
// caller has mapped RTP timestamps onto the unified line. Overlaps are merged
// so downstream diagnostics stay stable.
func PropagateMissing(trackID uint32, in []MissingRange) []MissingRange {
	out := make([]MissingRange, 0, len(in))
	for _, m := range in {
		if m.TrackID != 0 && m.TrackID != trackID {
			continue
		}
		m.TrackID = trackID
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartTicks < out[j].StartTicks })
	merged := out[:0]
	for _, m := range out {
		if n := len(merged); n > 0 && m.StartTicks <= merged[n-1].EndTicks+1 {
			if m.EndTicks > merged[n-1].EndTicks {
				merged[n-1].EndTicks = m.EndTicks
			}
			merged[n-1].Packets += m.Packets
			continue
		}
		merged = append(merged, m)
	}
	return merged
}
