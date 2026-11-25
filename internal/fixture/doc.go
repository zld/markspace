// Package fixture synthesises deterministic archive inputs: an MP4 document
// with per-track timescales and timed boxes, plus RTP packet streams for the
// normal, truncated, damaged (duplicate/reordered/burst loss) and boundary
// timestamp scenarios. Nothing is read from disk or the network; the same seed
// always yields byte-identical inputs.
package fixture

import "fmt"

// Kind selects which generated input a run consumes.
type Kind string

const (
	// KindNormal is a well-formed, in-order archive.
	KindNormal Kind = "normal"
	// KindTruncated cuts the MP4 structure mid-box.
	KindTruncated Kind = "truncated"
	// KindDamaged injects duplicates, reordering and burst loss into RTP and
	// also carries one attributed MP4 truncation, so the combined damaged
	// entry point reports both structural cut location and packet gaps.
	KindDamaged Kind = "damaged"
	// KindLate delivers events after their committed interval, forcing replay.
	KindLate Kind = "late"
	// KindBoundary pushes timestamps to wrap and range boundaries.
	KindBoundary Kind = "boundary"
)

// ParseKind validates a fixture name.
func ParseKind(s string) (Kind, error) {
	switch Kind(s) {
	case KindNormal, KindTruncated, KindDamaged, KindLate, KindBoundary:
		return Kind(s), nil
	default:
		return "", fmt.Errorf("unknown fixture %q (want normal, truncated, damaged, late or boundary)", s)
	}
}

// TrackDecl is one track's native timebase in the generated MP4.
type TrackDecl struct {
	TrackID   uint32
	Handler   string // ISOBMFF handler type
	Name      string // handler name string
	Timescale uint32
}

// DefaultTracks declares deliberately different timescales per track so the
// unified clock mapping is exercised end to end.
func DefaultTracks() []TrackDecl {
	return []TrackDecl{
		{TrackID: 1, Handler: "vide", Name: "video", Timescale: 30000},
		{TrackID: 2, Handler: "soun", Name: "audio", Timescale: 48000},
		{TrackID: 3, Handler: "subt", Name: "subtitle", Timescale: 1000},
		{TrackID: 4, Handler: "text", Name: "chapter", Timescale: 1000},
		{TrackID: 5, Handler: "meta", Name: "id3", Timescale: 1000},
		{TrackID: 6, Handler: "vide", Name: "sei", Timescale: 30000},
	}
}

// TimedItem is one generated timed box placed in a fragment.
type TimedItem struct {
	TrackID      uint32
	BoxType      string
	Presentation uint64
	Duration     uint32
	Sequence     uint16
	Text         string
	Payload      []byte
}
