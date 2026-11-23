package fixture

import (
	"github.com/pion/rtp"
)

// Packetize builds the RTP view of every segment. Video runs on the 90 kHz
// RTP clock; audio on its sample rate. The SSRC is derived from the
// representation so packet streams stay deterministic.
func Packetize(r *Representation) {
	ssrc := ssrcFromID(r.ID)
	globalSeq := uint16(0)
	for _, seg := range r.Segments {
		seqBase := globalSeq
		var packets []*rtp.Packet
		ts := uint32(seg.BaseDecode)
		n := len(seg.Samples)
		if r.Kind == "video" {
			n = packetCount(len(seg.Data))
		}
		for i := 0; i < n; i++ {
			marker := i == n-1
			p := &rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					PayloadType:    payloadType(r.Kind),
					SequenceNumber: seqBase + uint16(i),
					Timestamp:      ts,
					SSRC:           ssrc,
					Marker:         marker,
				},
				Payload: []byte{byte(i), byte(seg.Sequence)},
			}
			packets = append(packets, p)
			ts += uint32(r.SegmentDuration) / uint32(max1(n))
		}
		globalSeq = seqBase + uint16(n)
		seg.ExpectedPackets = n
		if seg.Truncated {
			// Tail loss: the last third of packets never arrives.
			packets = packets[:n*2/3]
		}
		if seg.Duplicate {
			packets = append(packets, packets[0])
		}
		seg.Packets = packets
	}
}

// packetCount maps byte size to a bounded, deterministic packet count.
func packetCount(dataLen int) int {
	n := dataLen / 1200
	if n < 4 {
		n = 4
	}
	if n > 24 {
		n = 24
	}
	return n
}

func payloadType(kind string) uint8 {
	if kind == "video" {
		return 96
	}
	return 97
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

func ssrcFromID(id string) uint32 {
	h := uint32(2166136261)
	for _, c := range id {
		h ^= uint32(c)
		h *= 16777619
	}
	return h
}
