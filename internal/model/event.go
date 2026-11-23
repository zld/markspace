package model

import "mediaqc/internal/clock"

// TrackKind discriminates media tracks.
type TrackKind string

const (
	TrackVideo TrackKind = "video"
	TrackAudio TrackKind = "audio"
)

// Gap is a declared, contiguous range of missing sequence numbers. Recovery
// packets are only ever allowed to fill ranges previously declared as gaps.
type Gap struct {
	TrackID     string
	FirstSeq    uint64
	LastSeq     uint64 // inclusive
	Recovered   bool
	DeclaredBy  Stage
}

// Count returns the number of missing packets in the gap.
func (g Gap) Count() uint64 { return g.LastSeq - g.FirstSeq + 1 }

// Event is the unified representation of one fragment/packet crossing the
// pipeline, independent of whether it came from HLS, DASH, MP4 or RTP.
type Event struct {
	MaterialID string
	TrackID    string
	Kind       TrackKind

	// Sequence is the source ordering key (RTP sequence / segment index).
	Sequence uint64
	// Discontinuity marks an HLS #EXT-X-DISCONTINUITY boundary.
	Discontinuity bool
	// Recovered marks events synthesized by the FEC layer.
	Recovered bool
	// FillsGapSeq is set on recovered events and must reference an existing
	// declared gap; the pipeline invariant checker enforces this.
	FillsGapSeq uint64
	HasGapRef   bool

	// Timing in the source track timebase; Start may be non-zero.
	Timebase  clock.Timebase
	Start     clock.Instant
	Duration  clock.Instant // duration expressed in the same timebase
	PTS       clock.Instant

	// Payload bounds: parsers cap sizes so this cannot exhaust memory.
	Payload []byte
	Size    int

	// Origin is adapter-specific provenance (file URI/SSRC etc.).
	Origin string
}

// End returns start+duration in the event's own timebase.
func (e Event) End() clock.Instant {
	return e.Timebase.At(e.Start.Ticks() + e.Duration.Ticks())
}

// TrackError is a convenience constructor scoped to one track.
func TrackError(code Code, stage Stage, materialID, trackID, msg string) *MediaError {
	return NewError(code, stage, materialID, trackID, msg)
}
