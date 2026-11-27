// Package ingest reads generated RTP packet streams and MP4 containers,
// validating structure and reporting errors with stage and offset.
package ingest

import (
	"github.com/pion/rtp"

	merr "medialib/internal/errors"
)

// Packet is one parsed RTP packet carrying an audio access unit.
type Packet struct {
	Seq       uint16 // raw RTP sequence number
	Timestamp uint32 // RTP clock ticks of the first sample
	Payload   []byte
	Arrival   int // arrival order in the received stream
}

// ParseRTP decodes one raw RTP datagram. The arrival index is recorded
// so reorder errors can be traced back to the input stream.
func ParseRTP(raw []byte, arrival int) (Packet, error) {
	var p rtp.Packet
	if err := p.Unmarshal(raw); err != nil {
		return Packet{}, merr.New(merr.StageIngest, int64(arrival), "rtp-unmarshal", err)
	}
	if len(p.Payload) == 0 {
		return Packet{}, merr.Newf(merr.StageIngest, int64(arrival), "rtp-unmarshal", "empty payload")
	}
	return Packet{
		Seq:       p.SequenceNumber,
		Timestamp: p.Timestamp,
		Payload:   p.Payload,
		Arrival:   arrival,
	}, nil
}

// StreamSpec describes a synthetic RTP stream fixture.
type StreamSpec struct {
	ClockRate    uint32   // RTP clock rate (ticks per second)
	Packets      int      // number of distinct sequence numbers
	BaseSeq      uint16   // first sequence number
	BaseTS       uint32   // first RTP timestamp
	FrameSamples []uint32 // per-packet sample counts, cycled (Opus/AAC vary)
	DupSeq       []int    // packet indices sent twice (duplicates)
	DropSeq      []int    // packet indices never sent (loss)
	Seed         uint32   // payload generator seed
}

// FrameDur returns the sample count carried by packet i.
func (s StreamSpec) FrameDur(i int) int64 {
	return int64(s.FrameSamples[i%len(s.FrameSamples)])
}

// GenRTP builds the wire form of the stream: duplicates are emitted
// twice, dropped packets are skipped, and bursts of three are sent in
// reverse order to exercise the reorder window.
func GenRTP(spec StreamSpec) [][]byte {
	drop := map[int]bool{}
	for _, d := range spec.DropSeq {
		drop[d] = true
	}
	dup := map[int]bool{}
	for _, d := range spec.DupSeq {
		dup[d] = true
	}
	mk := func(i int) []byte {
		ts := spec.BaseTS
		for k := 0; k < i; k++ { // cumulative: frame durations vary per packet
			ts += uint32(spec.FrameDur(k))
		}
		p := rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				SequenceNumber: spec.BaseSeq + uint16(i),
				Timestamp:      ts,
				SSRC:           0x4d454441, // "MEDA"
				PayloadType:    111,
			},
			Payload: genPayload(spec.Seed, i, 24+int(spec.FrameDur(i))%40),
		}
		raw, err := p.Marshal()
		if err != nil {
			panic(err)
		}
		return raw
	}
	var out [][]byte
	for i := 0; i < spec.Packets; i++ {
		if drop[i] {
			continue
		}
		out = append(out, mk(i))
		if dup[i] {
			out = append(out, mk(i)) // exact retransmission
		}
	}
	// Reverse each disjoint burst of 3 to create reordering.
	for base := 0; base+3 <= len(out); base += 3 {
		out[base], out[base+2] = out[base+2], out[base]
	}
	return out
}

// genPayload produces deterministic pseudo-codec bytes for packet i.
func genPayload(seed uint32, i, n int) []byte {
	x := seed ^ uint32(i)*2654435761
	b := make([]byte, n)
	for j := range b {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		b[j] = byte(x)
	}
	return b
}
