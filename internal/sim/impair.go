package sim

import (
	"encoding/binary"
	"math/rand"

	"jbbench/internal/protocol"
)

// impair optionally damages a fresh datagram, returning the delivered bytes
// and the event kind the receiver is expected to classify it as.
func impair(buf []byte, pkt protocol.Packet, rng *rand.Rand,
	cfg Config) (EventKind, []byte) {
	n := protocol.Encode(buf, pkt)
	roll := rng.Float64()
	switch {
	case roll < cfg.CorruptRate:
		out := append([]byte(nil), buf[:n]...)
		// Flip payload bytes without updating the checksum.
		for k := 0; k < 3; k++ {
			idx := protocol.HeaderSize + rng.Intn(protocol.PayloadBytes)
			out[idx] ^= byte(1 + rng.Intn(255))
		}
		return EventCorrupt, out
	case roll < cfg.CorruptRate*2:
		out := append([]byte(nil), buf[:n]...)
		out[0] = protocol.Version + 31 // unknown version
		return EventBadVer, out
	case roll < cfg.CorruptRate*3:
		out := append([]byte(nil), buf[:n]...)
		// Illegal declared length combined with a short datagram.
		binary.BigEndian.PutUint16(out[16:18], protocol.PayloadBytes/2)
		return EventBadLen, out[:protocol.HeaderSize+protocol.PayloadBytes/2]
	default:
		return EventOK, append([]byte(nil), buf[:n]...)
	}
}

// CountByKind summarizes an event stream, mostly used by tests.
func CountByKind(events []Event) map[EventKind]int {
	m := make(map[EventKind]int)
	for _, e := range events {
		m[e.Kind]++
	}
	return m
}
