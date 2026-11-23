// Package fixture generates the deterministic, in-memory sample media used by
// the demo and every test. No external files are referenced: all manifests,
// ISO BMFF segments and RTP packets are synthesized from a numeric seed.
package fixture

import (
	"math/rand"

	"github.com/pion/rtp"

	"mediacheck/internal/model"
)

// TrackMeta describes the normalization-relevant metadata of one track.
type TrackMeta struct {
	Channels       int    `json:"channels"`
	SampleRate     int    `json:"sample_rate_hz"`
	ColorPrimaries uint16 `json:"color_primaries"`
	TransferChar   uint16 `json:"transfer_characteristics"`
	MatrixCoeffs   uint16 `json:"matrix_coefficients"`
	UnknownColor   bool   `json:"color_metadata_unknown"`
}

// Sample is one coded media sample inside a segment.
type Sample struct {
	IsKeyframe bool
	Duration   int64 // in the representation timescale
	Bytes      int
	Quality    float64
}

// Segment is one media segment plus its packetized RTP view.
type Segment struct {
	Sequence     uint32
	Timescale    int64
	BaseDecode   int64 // tfdt baseMediaDecodeTime in timescale units
	Duration     int64 // timescale units
	IndexPresent bool
	Samples      []Sample
	Packets      []*rtp.Packet
	// ExpectedPackets is the count before truncation-driven tail loss.
	ExpectedPackets int
	Data            []byte // fMP4 byte range (styp+moof+mdat, optional sidx)
	Late            bool
	Duplicate       bool
	Truncated       bool
}

// Representation is one DASH adaptation set entry / HLS variant level.
type Representation struct {
	ID              string
	Kind            string // "video" or "audio"
	Timescale       int64
	TargetBitrate   int64 // bits per second
	SegmentDuration int64 // timescale units per nominal segment
	Meta            TrackMeta
	Init            []byte // fMP4 initialization segment (ftyp+moov)
	Segments        []*Segment
	// MPD/HLS manifest view, in master ticks (filled by Generate).
	ManifestStarts []int64
}

// Program is the full multi-bitrate fixture.
type Program struct {
	Seed   int64
	Video  []*Representation
	Audio  []*Representation
	Faults []model.FaultKind
}

// RTPClockRate is the packetization clock for video representations.
const RTPClockRate = 90000

// rngFrom returns a deterministic RNG derived from seed and a label so that
// independent streams never share a sequence draw.
func rngFrom(seed int64, label string) *rand.Rand {
	h := uint64(seed) * 1099511628211
	for _, c := range label {
		h ^= uint64(c)
		h *= 1099511628211
	}
	return rand.New(rand.NewSource(int64(h)))
}

// nominalSegmentCount keeps the demo small yet meaningful.
const nominalSegmentCount = 8
