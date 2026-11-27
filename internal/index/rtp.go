package index

import (
	"github.com/pion/rtp"

	mediaerr "mediacheck/internal/errors"
	"mediacheck/internal/timeline"
)

// RTPClockTolerance is the default RTP timestamp mismatch tolerance applied
// when an index entry is corroborated by an RTP sidecar packet.
const RTPClockTolerance uint32 = 1

// RTPSample is a parsed RTP packet carrying the sequence number and RTP
// timestamp an acquisition side may provide alongside the keyframe index.
type RTPSample struct {
	Sequence uint16
	TS       uint32
	SSRC     uint32
	Marker   bool
}

// ParseRTPPacket decodes one RTP packet using pion/rtp. The kernel never
// assumes RTP is present; this helper is only used when a sidecar exists.
func ParseRTPPacket(pkt []byte) (RTPSample, error) {
	p := &rtp.Packet{}
	if err := p.Unmarshal(pkt); err != nil {
		return RTPSample{}, mediaerr.Wrap(err, mediaerr.StageIndex, "",
			"invalid RTP packet")
	}
	return RTPSample{
		Sequence: p.SequenceNumber,
		TS:       p.Timestamp,
		SSRC:     p.SSRC,
		Marker:   p.Marker,
	}, nil
}

// RTPOrdering flags gaps or reordering in a sequence of RTP samples, which
// indicate the acquisition index may be incomplete. It returns the number of
// sequence discontinuities (each missing or out-of-order step counts once).
func RTPOrdering(samples []RTPSample) (gaps int) {
	for i := 1; i < len(samples); i++ {
		if samples[i].Sequence != samples[i-1].Sequence+1 {
			gaps++
		}
	}
	return gaps
}

// RTPTimestampMatches reports whether an RTP timestamp corroborates an
// expected value within the clock tolerance, handling 2^32 clock wrap.
func RTPTimestampMatches(got, want uint32, tolerance uint32) bool {
	if tolerance == 0 {
		tolerance = RTPClockTolerance
	}
	return diffU32(got, want) <= tolerance
}

// diffU32 returns the absolute circular distance between two uint32 values.
func diffU32(a, b uint32) uint32 {
	if a > b {
		if a-b <= 1<<31 {
			return a - b
		}
		return b - a
	}
	if b-a <= 1<<31 {
		return b - a
	}
	return a - b
}

// MarkerEntries converts marker-bearing RTP samples into provisional index
// entries stamped in the supplied time base. Byte offsets are left at -1
// because RTP alone does not carry elementary-stream byte positions; the
// pipeline fills them when reconciling against scanned keyframes.
func MarkerEntries(track string, samples []RTPSample,
	base timeline.TimeBase) []Entry {
	entries := make([]Entry, 0, len(samples))
	for _, s := range samples {
		if !s.Marker {
			continue
		}
		entries = append(entries, Entry{
			Track:      track,
			TS:         int64(s.TS),
			Base:       base,
			ByteOffset: -1,
		})
	}
	return entries
}
