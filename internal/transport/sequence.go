// Package transport reconstructs ordered, de-duplicated packet streams
// from lossy, reordered RTP transport. It uses pion/rtp for packet
// (de)serialization, sequence numbers and timestamps; the bounded
// reorder window and gap detection live in reorder.go.
package transport

import (
	"github.com/pion/rtp"

	"archivecheck/internal/merr"
)

// Envelope binds an RTP packet to the track/segment it belongs to. The
// fixture generator emits envelopes; downstream stages consume them per
// segment stream.
type Envelope struct {
	TrackID    uint32
	SegmentSeq uint32
	Packet     *rtp.Packet
}

// UnmarshalEnvelope parses one RTP datagram and tags it. Malformed
// datagrams are reported as decode errors, never silently dropped.
func UnmarshalEnvelope(buf []byte, trackID, segmentSeq uint32) (Envelope, error) {
	pkt := &rtp.Packet{}
	if err := pkt.Unmarshal(buf); err != nil {
		return Envelope{}, merr.New(merr.KindDecode, "transport",
			"track:"+u32(trackID)+":seq:"+u32(segmentSeq),
			"malformed RTP packet: "+err.Error())
	}
	return Envelope{TrackID: trackID, SegmentSeq: segmentSeq, Packet: pkt}, nil
}

// UnwrapSeq extends a 16-bit RTP sequence number relative to the highest
// unwrapped value seen so far, handling one single 16-bit wrap.
func UnwrapSeq(high uint32, seq16 uint16) uint32 {
	delta := int32(int16(seq16 - uint16(high)))
	return uint32(int64(high) + int64(delta))
}

// Seq16 returns the low 16 bits of an unwrapped sequence number.
func Seq16(seq uint32) uint16 { return uint16(seq) }

// CmpSeq compares two 16-bit sequence numbers modulo 2^16.
// It returns -1, 0 or 1.
func CmpSeq(a, b uint16) int {
	d := int16(a - b)
	switch {
	case d < 0:
		return -1
	case d > 0:
		return 1
	default:
		return 0
	}
}

func u32(v uint32) string {
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	var buf [10]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = digits[v%10]
		v /= 10
	}
	return string(buf[i:])
}
