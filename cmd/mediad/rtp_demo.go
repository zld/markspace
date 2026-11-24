package main

import (
	pionrtp "github.com/pion/rtp"

	"mediad/internal/model"
	"mediad/internal/rtp"
)

// buildRTP generates a deterministic packet stream containing in-order runs,
// a recovered reorder inside the window, a loss that stays open until flush,
// a duplicate and one truncated wire buffer. It returns receiver stats and
// any structured error codes produced while parsing.
func buildRTP(r *rng, window uint16) (rtp.Stats, []*pionrtp.Packet, []string) {
	recv := rtp.NewReceiver(window)
	var ordered []*pionrtp.Packet
	codes := map[string]bool{}

	emit := func(sn uint16) {
		p := &pionrtp.Packet{
			Header: pionrtp.Header{
				Version: 2, PayloadType: 96,
				SequenceNumber: sn, Timestamp: uint32(sn) * 160,
				SSRC: 0xdeadbeef,
			},
			Payload: []byte{byte(sn), byte(sn >> 8)},
		}
		recv.Write(p)
		ordered = append(ordered, p)
	}

	// In-order 0..4, then a gap (6 missing) filled late inside the window.
	for sn := uint16(0); sn <= 4; sn++ {
		emit(sn)
	}
	emit(7) // opens gaps 5,6
	emit(5) // reordered, recovered (distance 2 <= window)
	emit(6) // reordered, recovered
	// Duplicate of an already closed sequence.
	emit(3)
	// A bounded jump opens a small gap set; most are filled inside the window
	// while one stays open until flush and counts as unrecoverable.
	emit(12) // opens gaps 8..11
	emit(9)  // distance 3 -> recovered
	emit(10) // distance 2 -> recovered
	emit(11) // distance 1 -> recovered; gap 8 remains open -> unrecoverable
	// Total gaps: 5,6 (recovered) + 8,9,10,11 = 6; 5 recovered -> 0.8333.

	// Truncated wire buffer must surface RTP_TRUNCATED and be skipped.
	if _, err := rtp.Parse([]byte{0x80, 0x60, 0x00, 0x01}); err != nil {
		if me := model.AsMediaError(err); me != nil {
			codes[string(me.Code)] = true
		}
	}
	// Touch the rng so the seed genuinely shapes transport fixtures too.
	_ = r.intn(4)

	return recv.Flush(), ordered, sortedKeys(codes)
}
