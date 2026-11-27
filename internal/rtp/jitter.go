// Package rtp recovers an ordered media timeline from RTP packets.
// The jitter buffer tolerates sequence wrap, reordering, duplicates and
// loss bursts; the recoverer records missing ranges and recovery
// positions. Late packets are counted and discarded — they never
// overwrite the already-committed timeline.
package rtp

import (
	pionrtp "github.com/pion/rtp"
)

// DefaultMaxGap is the number of missing sequence numbers tolerated
// before a gap is declared lost and the stream recovers past it.
const DefaultMaxGap = 2

// Range is an inclusive interval of extended sequence numbers.
type Range struct {
	From uint64 `json:"from"`
	To   uint64 `json:"to"`
}

// Recovery marks where the committed timeline resumed after a loss.
type Recovery struct {
	Seq          uint64 `json:"seq"`
	RTPTimestamp uint32 `json:"rtp_timestamp"`
}

// Stats counts packet-level outcomes of the recovery run.
type Stats struct {
	Received   int
	Lost       int
	Duplicated int
	Late       int
	Missing    []Range
	Recoveries []Recovery
}

// Committed is a packet that left the jitter buffer in order.
type Committed struct {
	Ext    uint64 // extended sequence number
	Packet *pionrtp.Packet
}

// jitterBuffer holds out-of-order packets keyed by extended sequence
// number. Its occupancy is bounded because the recoverer declares a
// loss and jumps forward as soon as a gap exceeds maxGap.
type jitterBuffer struct {
	pkts map[uint64]*pionrtp.Packet
}

func newJitterBuffer() *jitterBuffer {
	return &jitterBuffer{pkts: make(map[uint64]*pionrtp.Packet)}
}

func (b *jitterBuffer) min() (uint64, bool) {
	var m uint64
	ok := false
	for s := range b.pkts {
		if !ok || s < m {
			m = s
			ok = true
		}
	}
	return m, ok
}
