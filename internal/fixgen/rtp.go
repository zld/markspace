// Package fixgen generates deterministic local RTP/MP4 sample fixtures.
// No external media assets are used; everything is synthesized from a seed.
package fixgen

import (
	"math/rand"

	"github.com/pion/rtp"
)

// RTPParams controls RTP fixture synthesis.
type RTPParams struct {
	Seed          int64
	Count         int    // number of base packets
	StartSeq      uint16 // initial sequence number (choose near 65535 to force wrap)
	StartTS       uint32 // initial RTP timestamp
	SamplesPerPkt uint32 // RTP timestamp step per packet
	LossEvery     int    // drop every Nth packet (0 disables)
	DupEvery      int    // duplicate every Nth packet (0 disables)
	ReorderEvery  int    // swap every Nth packet with its successor (0 disables)
	TruncateAt    int    // emit a truncated datagram at this index (-1 disables)
	BadOpusAt     int    // emit an undecodable Opus payload at this index (-1 disables)
	PayloadSize   int    // Opus payload bytes per packet (default 5)
}

// GenRTP builds a datagram stream (in arrival order) carrying minimal Opus
// packets (TOC 0xF8: CELT 20 ms, one frame) with loss, duplication,
// reordering, wraparound and truncation as configured.
func GenRTP(p RTPParams) [][]byte {
	rng := rand.New(rand.NewSource(p.Seed))
	var out [][]byte
	for i := 0; i < p.Count; i++ {
		seq := p.StartSeq + uint16(i)
		ts := p.StartTS + uint32(i)*p.SamplesPerPkt
		size := p.PayloadSize
		if size <= 0 {
			size = 5
		}
		payload := make([]byte, size)
		payload[0] = 0xF8 // Opus TOC: config 31 (CELT 20 ms), code 0
		rng.Read(payload[1:])
		if p.BadOpusAt >= 0 && i == p.BadOpusAt {
			payload = []byte{0x03} // TOC code 3 with missing frame-count byte
		}
		pkt := &rtp.Packet{
			Header:  rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: ts, SSRC: 0xC0FFEE},
			Payload: payload,
		}
		raw, err := pkt.Marshal()
		if err != nil {
			panic(err)
		}
		if p.TruncateAt >= 0 && i == p.TruncateAt {
			raw = raw[:8] // shorter than the 12-byte RTP header: unparseable
		}
		if p.LossEvery > 0 && i%p.LossEvery == 0 {
			continue // lost in transit
		}
		out = append(out, raw)
		if p.DupEvery > 0 && i%p.DupEvery == 0 {
			out = append(out, raw) // duplicate arrival
		}
		if p.ReorderEvery > 0 && i%p.ReorderEvery == 0 && len(out) >= 2 {
			// Swap with the previously emitted datagram (out-of-order arrival).
			out[len(out)-1], out[len(out)-2] = out[len(out)-2], out[len(out)-1]
		}
	}
	return out
}
