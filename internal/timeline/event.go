// Package timeline ingests ordered and out-of-order sample events, advances a
// monotonic watermark, repairs clock wraps/source-switch gaps and replays
// deterministically from checkpoints when a late event arrives. All time is
// integer master-tick math; raw values are always preserved.
package timeline

import (
	"mediapack/internal/model"
)

// Event is one timeline input. Seq is the globally unique, externally assigned
// arrival order used for both deduplication and deterministic adjudication.
type Event struct {
	Seq         uint64 `json:"seq"`
	Source      string `json:"source"`
	TrackID     uint32 `json:"trackId"`
	SampleIndex uint32 `json:"sampleIndex"`
	// OriginalTick is the raw master-tick timestamp before any repair.
	OriginalTick int64 `json:"originalTick"`
	Duration     int64 `json:"durationTick"`
	// RTPRaw, when present, is the 32-bit RTP timestamp used to detect wraps.
	RTPRaw uint32 `json:"rtpRaw,omitempty"`
	HasRTP bool   `json:"hasRtp"`
	// Late marks an event delivered after the watermark already passed its
	// timestamp. Such an event cannot be appended; it forces a deterministic
	// checkpoint replay. Normal in-order events leave this false.
	Late bool `json:"late,omitempty"`
}

// Rule configures source-switch adjudication and wrap handling.
type Rule struct {
	// MaxGapTick: gaps no larger than this are tolerated and left as silence;
	// larger gaps raise CodeGap. Negative disables the check entirely.
	MaxGapTick int64
	// RejectOverlap, when true, raises CodeOverlap instead of dropping the
	// later (higher Seq) of two overlapping events.
	RejectOverlap bool
}

// StandardRule returns the demo defaults: bounded gaps, overlap dropped by Seq.
func StandardRule() Rule {
	return Rule{MaxGapTick: 7200, RejectOverlap: false}
}

// Boundary is the timeline's repaired view of one event's edge.
type Boundary = model.Boundary

// Repair kinds recorded on boundaries and in the journal.
const (
	RepairNone    = ""
	RepairWrap    = "clock-wrap"
	RepairOverlap = "overlap-drop"
	RepairGap     = "gap-tolerated"
	RepairLate    = "late-replay"
	RepairReorder = "monotonic-clamp"
)

// key uniquely identifies an input sample for deduplication.
func (e Event) key() string {
	return eventKey(e.Source, e.TrackID, e.SampleIndex)
}

func eventKey(source string, track, index uint32) string {
	return source + "#" + u32s(track) + "#" + u32s(index)
}

func u32s(v uint32) string {
	if v == 0 {
		return "0"
	}
	var b [10]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
