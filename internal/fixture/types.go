package fixture

import (
	"mediatool/internal/mp4"
	clockrtp "mediatool/internal/rtp"

	pionrtp "github.com/pion/rtp"
)

// TrackSpec configures one generated track.
type TrackSpec struct {
	TrackID   uint32
	Timescale uint32
	MediaType string
	CodecKey  string
	// SampleDuration is the per-sample duration in track units; variation
	// is applied by the generator when VariableRate is set.
	SampleDuration uint32
	VariableRate   bool
}

// Fragment is one generated chunk plus the metadata the pipeline needs.
type Fragment struct {
	Seq       int
	TrackID   uint32
	DTS       uint64
	Bytes     []byte
	Durations []uint32
	Sizes     []uint32
	Payload   []byte
}

// Group bundles a full deterministic dataset.
type Group struct {
	Seed       int64
	Init       []byte
	Tracks     []mp4.TrackInfo
	VideoFrags []Fragment
	AudioFrags []Fragment
	// Corrupted holds byte slices that must be rejected by the parser.
	Corrupted []CorruptCase
	RTPVideo  []*pionrtp.Packet
	RTPEvents []clockrtp.Observation
	// LateAt marks the input order at which a fragment is delivered late.
	LateAt int
	// Target bitrates in bits per second, shared with the metrics module.
	VideoTargetBPS int
	AudioTargetBPS int
}

// CorruptCase pairs a malformed byte slice with the expected error class.
type CorruptCase struct {
	Name  string
	Bytes []byte
	Class string
}

// Alignment grid: every fragment spans exactly GridMS milliseconds, which
// both tracks express exactly on their own timescale (5760 units at
// 90 kHz; 3072 = 3x1024 units at 48 kHz). This mirrors a real capture
// schedule and keeps inter-track alignment error at zero.
const GridMS = 64

// Target bitrates chosen so a grid fragment carries a whole number of
// compressed payload bytes: video 7200 B/s, audio 1152 B/s per window.
const (
	VideoTargetBPS = 900_000
	AudioTargetBPS = 144_000
)
