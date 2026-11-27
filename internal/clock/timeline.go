package clock

import "math/bits"

// Mapping converts source ticks to the unified nanosecond timeline:
//
//	ns = tick * Num / Den + Offset
//
// Num/Den is the explicit rational scale (nanoseconds per tick). Both must be
// positive; a zero component is rejected with ErrZeroScale. All arithmetic is
// overflow-checked; unrepresentable results yield ErrOverflow.
type Mapping struct {
	Num    int64
	Den    int64
	Offset int64
}

// NewMapping validates and returns a Mapping. Zero or negative scale
// components are rejected.
func NewMapping(num, den, offset int64) (Mapping, error) {
	if num <= 0 || den <= 0 {
		return Mapping{}, New(ErrZeroScale, "scale must be positive, got %d/%d", num, den)
	}
	return Mapping{Num: num, Den: den, Offset: offset}, nil
}

// Map converts one tick value to nanoseconds on the unified timeline.
func (m Mapping) Map(tick int64) (int64, error) {
	if m.Num <= 0 || m.Den <= 0 {
		return 0, New(ErrZeroScale, "mapping not initialized")
	}
	// tick*Num with overflow check.
	hi, lo := bits.Mul64(uint64(tick), uint64(m.Num))
	if hi != 0 {
		return 0, New(ErrOverflow, "tick*num overflows: %d*%d", tick, m.Num)
	}
	scaled := int64(lo)
	if scaled < 0 {
		return 0, New(ErrOverflow, "tick*num overflows int64: %d*%d", tick, m.Num)
	}
	q := scaled / m.Den
	// q + Offset with overflow check.
	sum := q + m.Offset
	if (m.Offset > 0 && sum < q) || (m.Offset < 0 && sum > q) {
		return 0, New(ErrOverflow, "offset addition overflows: %d+%d", q, m.Offset)
	}
	return sum, nil
}

// Timeline binds a unified nanosecond base to per-track mappings. Tracks
// without a registered mapping are rejected rather than silently guessed.
type Timeline struct {
	track map[uint64]Mapping
}

// NewTimeline returns an empty Timeline.
func NewTimeline() *Timeline {
	return &Timeline{track: make(map[uint64]Mapping)}
}

// Bind registers a mapping for a track. Rebinding with a different scale is
// allowed only if identical, to keep reruns deterministic.
func (t *Timeline) Bind(track uint64, m Mapping) error {
	if prev, ok := t.track[track]; ok && prev != m {
		return New(ErrUnmappableTrack, "track %d rebound with different scale", track)
	}
	if m.Num <= 0 || m.Den <= 0 {
		return New(ErrZeroScale, "track %d has non-positive scale", track)
	}
	t.track[track] = m
	return nil
}

// Map converts (track, tick) to unified nanoseconds.
func (t *Timeline) Map(track uint64, tick int64) (int64, error) {
	m, ok := t.track[track]
	if !ok {
		return 0, New(ErrUnmappableTrack, "track %d has no registered mapping", track)
	}
	return m.Map(tick)
}

// Has reports whether a track mapping exists.
func (t *Timeline) Has(track uint64) bool {
	_, ok := t.track[track]
	return ok
}

// MatroskaMapping builds the mapping for a Matroska track: ticks are in
// TimecodeScale units (nanoseconds per tick, default 1_000_000), optionally
// scaled by the per-track TrackTimecodeScale expressed as a rational
// num/den (the spec's float is almost always 1.0; callers pass 1/1).
func MatroskaMapping(timecodeScale uint64, trackScaleNum, trackScaleDen int64, offsetNs int64) (Mapping, error) {
	if timecodeScale == 0 {
		return Mapping{}, New(ErrZeroScale, "timecode scale is zero")
	}
	if trackScaleNum <= 0 || trackScaleDen <= 0 {
		return Mapping{}, New(ErrZeroScale, "track scale must be positive")
	}
	num := uint64(timecodeScale) * uint64(trackScaleNum)
	hi, _ := bits.Mul64(uint64(timecodeScale), uint64(trackScaleNum))
	if hi != 0 || num > uint64(1<<62) {
		return Mapping{}, New(ErrOverflow, "timecode scale product overflows")
	}
	return NewMapping(int64(num), trackScaleDen, offsetNs)
}

// RTPMapping builds the mapping for RTP capture events: ns = ts * 1e9 / clockRate.
func RTPMapping(clockRate uint32, offsetNs int64) (Mapping, error) {
	if clockRate == 0 {
		return Mapping{}, New(ErrZeroScale, "RTP clock rate is zero")
	}
	return NewMapping(1_000_000_000, int64(clockRate), offsetNs)
}
