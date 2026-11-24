package ingest

import (
	"github.com/pion/rtp/v2"

	"mediamerge/internal/model"
)

// RTPEvent is one parsed RTP packet from a capture sidecar, with its
// 32-bit timestamp unrolled into a 64-bit monotone value.
type RTPEvent struct {
	Seq             uint16
	Timestamp       uint32 // raw header timestamp
	Unrolled        uint64 // rollover-corrected timestamp
	PayloadBytes    int
	HeaderExtension bool
}

// ParseRTP parses a length-prefixed RTP capture using pion/rtp header
// handling. Malformed packets produce locatable recoverable diagnostics;
// parsing resumes at the next length prefix.
func ParseRTP(path string, data []byte, errs *model.Errors) []RTPEvent {
	var events []RTPEvent
	var last uint64
	var rollover uint64
	haveLast := false
	off := 0
	for off+2 <= len(data) {
		n := int(data[off])<<8 | int(data[off+1])
		if n <= 0 || off+2+n > len(data) {
			errs.Add(model.NewError(model.LevelRecovered, "rtp", path, int64(off), 0,
				"rtp record truncated or invalid length %d", n))
			break
		}
		var pkt rtp.Packet
		if err := pkt.Unmarshal(data[off+2 : off+2+n]); err != nil {
			errs.Add(model.NewError(model.LevelRecovered, "rtp", path, int64(off+2), 0,
				"rtp header rejected: %v", err))
			off += 2 + n
			continue
		}
		ts := uint64(pkt.Timestamp)
		if haveLast {
			prev32 := uint32(last & 0xffffffff)
			// Standard RFC 3550 rollover: forward wrap within half range.
			if pkt.Timestamp < prev32 && prev32-pkt.Timestamp > 1<<31 {
				rollover += 1 << 32
			}
		}
		unrolled := rollover + ts
		if haveLast && unrolled < last {
			unrolled = last // clamp: arrival order is authoritative
		}
		events = append(events, RTPEvent{
			Seq:             pkt.SequenceNumber,
			Timestamp:       pkt.Timestamp,
			Unrolled:        unrolled,
			PayloadBytes:    len(pkt.Payload),
			HeaderExtension: pkt.Extension,
		})
		last = unrolled
		haveLast = true
		off += 2 + n
	}
	return events
}

// Monotonic reports whether the unrolled RTP timeline is non-decreasing.
func Monotonic(events []RTPEvent) bool {
	for i := 1; i < len(events); i++ {
		if events[i].Unrolled < events[i-1].Unrolled {
			return false
		}
	}
	return true
}
