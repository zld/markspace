// Package model defines the shared media-processing data model: the common
// integer time base, the unified segment descriptor exchanged between every
// subsystem, and the locatable error taxonomy.
package model

import "math"

// TimeBaseNanoseconds is the shared integer time base every representation is
// mapped onto. All timestamps that cross subsystem boundaries are int64
// nanoseconds so that different DASH timescales and RTP clock rates can be
// compared exactly without floating point drift.
const TimeBaseNanoseconds int64 = 1_000_000_000

// AssetKind identifies the logical track a segment belongs to.
type AssetKind uint8

const (
	KindDialogue AssetKind = iota // spoken dialogue (core language track)
	KindSubtitle                  // subtitle/caption text
	KindSFX                       // sound effect
	KindVideo                     // picture track, carries keyframes
)

// String renders the asset kind for deterministic reports.
func (k AssetKind) String() string {
	switch k {
	case KindDialogue:
		return "dialogue"
	case KindSubtitle:
		return "subtitle"
	case KindSFX:
		return "sfx"
	case KindVideo:
		return "video"
	default:
		return "unknown"
	}
}

// Priority is the fixed resource-conflict ordering: quality gate first,
// processing budget second, delivery size last. Core language dialogue and
// subtitles must always remain complete.
type Priority uint8

const (
	PriorityCore Priority = iota // dialogue + subtitle, never dropped
	PriorityHigh                 // primary video representation
	PriorityLow                  // alternate bitrate representations / non-core audio
)

// SegmentDescriptor is the unified fragment description produced by both the
// DASH and HLS parsers and consumed by the RTP, sync, segment and verify
// subsystems. All timing fields use the shared nanosecond base.
type SegmentDescriptor struct {
	Source         string    // manifest origin identifier, e.g. "dash" or "hls"
	Representation string    // representation / bitrate layer id
	PeriodID       int       // DASH period or advanced HLS discontinuity sequence
	Index          int       // zero-based segment index inside its period
	Kind           AssetKind // logical track
	Keyframe       bool      // segment starts on a keyframe (sync sample)
	StartNS        int64     // inclusive start time on the shared clock, ns
	DurationNS     int64     // positive duration, ns
	Timescale      uint32    // source timescale that generated Start/Duration
	StartInScale   int64     // original start timestamp in Timescale units
	DurationScale  int64     // original duration in Timescale units
	Payload        []byte    // optional payload bytes (fixture data)
}

// EndNS returns the exclusive end time.
func (s SegmentDescriptor) EndNS() int64 { return s.StartNS + s.DurationNS }

// ScaleToNanoseconds maps a non-negative timestamp expressed in timescale
// units onto the shared nanosecond base. It rejects a zero timescale, negative
// input and int64 overflow, returning a classified MediaError.
func ScaleToNanoseconds(value int64, timescale uint32) (int64, error) {
	if timescale == 0 {
		return 0, NewError(KindInvalidTimescale, "model", 0,
			"zero timescale cannot be mapped to the shared time base")
	}
	if value < 0 {
		return 0, NewError(KindNegativeDuration, "model", 0,
			"negative timestamp %d in timescale %d", value, timescale)
	}
	// value * 1e9 must fit in int64; divide first, then check the remainder.
	hi := value / int64(timescale)
	rem := value % int64(timescale)
	if hi > math.MaxInt64/TimeBaseNanoseconds {
		return 0, NewError(KindOverflow, "model", 0,
			"timestamp %d overflows the nanosecond base", value)
	}
	ns := hi * TimeBaseNanoseconds
	add := (rem * TimeBaseNanoseconds) / int64(timescale)
	if ns > math.MaxInt64-add {
		return 0, NewError(KindOverflow, "model", 0,
			"timestamp %d overflows the nanosecond base", value)
	}
	return ns + add, nil
}

// ScaleDuration maps a duration and additionally rejects non-positive
// durations, which are illegal for a segment.
func ScaleDuration(duration int64, timescale uint32) (int64, error) {
	if duration <= 0 {
		return 0, NewError(KindNegativeDuration, "model", 0,
			"segment duration must be positive, got %d", duration)
	}
	return ScaleToNanoseconds(duration, timescale)
}
