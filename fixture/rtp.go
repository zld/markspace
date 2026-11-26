package fixture

import (
	"encoding/binary"
	"math/rand"

	"github.com/pion/rtp"
)

// RTP parameters of the generated capture.
const (
	RTPBaseSeq   = 1000
	RTPCount     = 24 // nominal packets: 1000..1023
	RTPClockRate = 90000
	RTPPacketMs  = 20
	RTPWindow    = 8
)

// RTPFaults describes the transport faults injected into a capture.
type RTPFaults struct {
	DuplicateSeq uint16 // this packet is transmitted twice
	ReorderBase  uint16 // base+2 and base+3 are sent before base+1
	LossBase     uint16 // lossBase..lossBase+2 are never sent (burst of 3)
}

// DefaultRTPFaults returns the standard fault injection: one duplicate,
// two in-window reorders, one burst of three lost packets.
func DefaultRTPFaults() RTPFaults {
	return RTPFaults{
		DuplicateSeq: RTPBaseSeq + 5,
		ReorderBase:  RTPBaseSeq + 10,
		LossBase:     RTPBaseSeq + 16,
	}
}

// BuildCapture generates a framed RTP capture: each record is a 2-byte
// big-endian length followed by one RTP packet. Arrival order reflects
// the injected faults; packet timestamps are 90kHz at 20ms per packet.
func BuildCapture(rnd *rand.Rand, faults *RTPFaults) ([]byte, error) {
	var order []uint16
	for i := 0; i < RTPCount; i++ {
		seq := uint16(RTPBaseSeq + i)
		if faults != nil {
			if seq >= faults.LossBase && seq < faults.LossBase+3 {
				continue // burst loss
			}
			if seq == faults.ReorderBase+1 {
				order = append(order, faults.ReorderBase+2, faults.ReorderBase+3)
			}
			if seq == faults.ReorderBase+2 || seq == faults.ReorderBase+3 {
				continue // already emitted out of order
			}
		}
		order = append(order, seq)
		if faults != nil && seq == faults.DuplicateSeq {
			order = append(order, seq) // network duplicate
		}
	}
	var out []byte
	for arrival, seq := range order {
		ts := uint32(90000 + int(seq-RTPBaseSeq)*RTPPacketMs*RTPClockRate/1000)
		p := rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				SequenceNumber: seq,
				Timestamp:      ts,
				SSRC:           0x5c0001,
			},
			Payload: randBytes(rnd, 800+rnd.Intn(800)),
		}
		raw, err := p.Marshal()
		if err != nil {
			return nil, err
		}
		var hdr [2]byte
		binary.BigEndian.PutUint16(hdr[:], uint16(len(raw)))
		out = append(out, hdr[:]...)
		out = append(out, raw...)
		_ = arrival
	}
	return out, nil
}
