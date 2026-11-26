// Package packet parses RTP packets and normalizes arrival disorder —
// duplicates, reordering and loss — into the shared error model.
package packet

import (
	"github.com/pion/rtp"

	"streamcheck/core"
)

// Record is one parsed RTP packet with capture metadata.
type Record struct {
	Seq       uint16
	Timestamp uint32
	SSRC      uint32
	Arrival   int   // arrival index in the capture
	ArrivalMs int64 // capture clock in milliseconds
	Size      int   // wire size in bytes
}

// Parse decodes one RTP packet from the wire.
func Parse(data []byte, arrival int, arrivalMs int64) (Record, error) {
	var p rtp.Packet
	if err := p.Unmarshal(data); err != nil {
		return Record{}, core.E(core.KindPacket, "parse-rtp", "malformed RTP packet", err)
	}
	return Record{
		Seq:       p.SequenceNumber,
		Timestamp: p.Timestamp,
		SSRC:      p.SSRC,
		Arrival:   arrival,
		ArrivalMs: arrivalMs,
		Size:      len(data),
	}, nil
}
