package sample

import (
	"encoding/binary"
	"hash/fnv"

	pionrtp "github.com/pion/rtp"
)

// LossWire returns a deterministic, stateless loss simulator: packets
// are dropped in bursts of `burst` consecutive sequence numbers whenever
// a keyed hash falls under the loss rate. Stateless matters: a crashed
// pipeline run that resumes sees exactly the same wire, so its output
// digest matches an uninterrupted run.
func LossWire(seed int64, loss float64, burst int) func(frame int, pkts []*pionrtp.Packet) []*pionrtp.Packet {
	if burst < 1 {
		burst = 1
	}
	return func(frame int, pkts []*pionrtp.Packet) []*pionrtp.Packet {
		var kept []*pionrtp.Packet
		for _, p := range pkts {
			block := int64(p.SequenceNumber) / int64(burst)
			if drop(seed, p.SSRC, block, loss) {
				continue
			}
			kept = append(kept, p)
		}
		return kept
	}
}

func drop(seed int64, ssrc uint32, block int64, loss float64) bool {
	var b [20]byte
	binary.LittleEndian.PutUint64(b[0:], uint64(seed))
	binary.LittleEndian.PutUint32(b[8:], ssrc)
	binary.LittleEndian.PutUint64(b[12:], uint64(block))
	h := fnv.New64a()
	h.Write(b[:])
	return float64(h.Sum64()%1_000_000)/1_000_000 < loss
}
