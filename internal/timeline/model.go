// Package timeline holds the shared domain model used by the decoder,
// repair logic, index and journal: tracks, timed events, source offsets,
// repair rules and structured errors that never lose the raw input offset.
package timeline

import (
	"errors"
	"fmt"
)

// EventKind distinguishes the metadata payloads handled on side tracks.
type EventKind string

const (
	KindSample   EventKind = "sample"   // regular media sample (video/audio)
	KindSubtitle EventKind = "subtitle" // WebVTT/TTML-style subtitle
	KindChapter  EventKind = "chapter"  // chapter marker
	KindID3      EventKind = "id3"      // RTP-carried ID3 tag
	KindEmsg     EventKind = "emsg"     // ISO-BMFF emsg box
	KindSEI      EventKind = "sei"      // H.264/H.265 SEI payload
)

// Source identifies one input stream (file or packet capture).
type Source struct {
	ID   string // stable logical name, e.g. "edge-3/video"
	Kind string // "mp4" or "rtp"
	// Fingerprint is the SHA-256 of the full input bytes, hex encoded.
	Fingerprint string
}

// Track is one media/metadata track with its original timescale preserved.
type Track struct {
	ID         string
	SourceID   string
	Timescale  uint32 // original track timescale, never rewritten
	ClockRate  uint32 // RTP clock rate (RTP sources only)
	IsKeyframe bool   // true for video tracks carrying sync samples
	NoKeyframe bool   // set by repair when a track has no sync sample at all
}

// OffsetRange is a byte span inside the original input, for provenance.
type OffsetRange struct {
	Begin int64
	End   int64 // exclusive
}

// Event is one timed unit. RawPayload keeps the original bytes; payloads are
// never fully aggregated by the pipeline (samples stream one at a time).
type Event struct {
	Kind        EventKind
	TrackID     string
	SourceID    string
	Seq         int    // decoder-assigned monotonic ordinal
	RTPSeq      uint16 // RTP sequence number (RTP sources)
	RawTS       int64  // timestamp in the track's original timescale
	ExtendedTS  int64  // unwrapped RTP timestamp (RTP sources only)
	Ticks       int64  // unified-tick timestamp after repair
	EndTicks    int64  // unified-tick end (samples with duration)
	Keyframe    bool   // sync sample flag
	RawPayload  []byte // original bytes, retained for hashing/provenance
	Offset      OffsetRange
	ContentHash string // SHA-256 hex of RawPayload, filled by decoder
	Deduped     bool   // true when a later equal event was dropped
	Conflict    bool   // cross-track conflict marker
	RuleID      string // repair rule that last touched this event
}

// RepairRule records one deterministic repair decision so every output tick
// can be traced back to the rule that produced it.
type RepairRule struct {
	ID         string
	TrackID    string
	Kind       string // "anchor-offset", "drift", "unwrap", "dedup"
	AnchorSeq  int    // anchor event ordinal, -1 when unused
	AnchorTick int64  // unified tick the anchor was pinned to
	OffsetTick int64  // constant offset applied in ticks
	SlopeNum   int64  // drift slope numerator
	SlopeDen   int64  // drift slope denominator
	Reason     string
}

// Structured error codes shared across subsystems.
const (
	CodeRolloverAmbiguous = "rollover-ambiguous"
	CodePacketLoss        = "packet-loss"
	CodeTruncatedBox      = "truncated-box"
	CodeTimescaleConflict = "timescale-conflict"
	CodeTruncatedIndex    = "truncated-index"
	CodeValidation        = "validation"
)

// Error is the only error type crossing subsystem boundaries. It always
// carries the stage, track (when known) and raw input offset so a fault
// reported by the index or timeline can still be located in the input.
type Error struct {
	Code    string
	Stage   string
	TrackID string
	Offset  int64
	Detail  string
	Inner   error
}

func (e *Error) Error() string {
	s := fmt.Sprintf("%s: stage=%s", e.Code, e.Stage)
	if e.TrackID != "" {
		s += " track=" + e.TrackID
	}
	s += fmt.Sprintf(" offset=%d: %s", e.Offset, e.Detail)
	if e.Inner != nil {
		s += ": " + e.Inner.Error()
	}
	return s
}

// Unwrap supports errors.Is/As on the wrapped cause.
func (e *Error) Unwrap() error { return e.Inner }

// NewError builds a structured error with an offset.
func NewError(code, stage, track string, offset int64, detail string, inner error) *Error {
	return &Error{Code: code, Stage: stage, TrackID: track, Offset: offset, Detail: detail, Inner: inner}
}

// ErrConflict is returned when cross-track events exceed tolerance; the
// pipeline must not publish an index in that state.
var ErrConflict = errors.New("timeline: cross-track conflict beyond tolerance")

// ErrNoKeyframe is the explicit status for tracks without sync samples.
var ErrNoKeyframe = errors.New("timeline: track contains no keyframe")

// Config drives alignment. All numeric tolerances are in unified ticks.
type Config struct {
	ToleranceTicks int64 // cross-track alignment tolerance
	AnchorEvery    int   // anchor density (events between anchors)
	DriftSlopeNum  int64
	DriftSlopeDen  int64
	// ConfigVersion lets callers bump logic explicitly; content hash also
	// participates in checkpoint invalidation.
	ConfigVersion int
}

// DefaultConfig gives a 50ms cross-track tolerance.
func DefaultConfig() Config {
	return Config{
		ToleranceTicks: 50_000_000,
		AnchorEvery:    16,
		DriftSlopeNum:  1,
		DriftSlopeDen:  1,
		ConfigVersion:  1,
	}
}
