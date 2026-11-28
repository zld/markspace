package stream

import (
	"encoding/binary"

	"mediasync/internal/fault"
)

// XOR frame payload layout (all integers big-endian):
//
//	uint32 baseSeq   - first sequence number covered by the frame
//	uint32 count     - number of RTP packets covered (1..65535)
//	uint32 payloadLen- fixed payload length used by every covered packet
//	bytes  xor       - payloadLen bytes, the XOR of all covered payloads
const xorHeaderSize = 12

// XORFrame is a parsed single-packet XOR redundancy frame.
type XORFrame struct {
	BaseSeq    uint16
	Count      uint32
	PayloadLen uint32
	XOR        []byte
	Offset     int64
}

// ParseXOR decodes every kind-1 record and validates its header.
func ParseXOR(frames []Record) ([]XORFrame, error) {
	out := make([]XORFrame, 0, len(frames))
	for _, rec := range frames {
		if len(rec.Payload) < xorHeaderSize {
			return nil, fault.New(fault.ErrPacketMalformed, "stream", rec.Offset,
				"xor frame needs %d header bytes, got %d", xorHeaderSize, len(rec.Payload))
		}
		base := binary.BigEndian.Uint32(rec.Payload[0:4])
		count := binary.BigEndian.Uint32(rec.Payload[4:8])
		payloadLen := binary.BigEndian.Uint32(rec.Payload[8:12])
		if count == 0 || count > 65535 || payloadLen == 0 || payloadLen > MaxFrameSize {
			return nil, fault.New(fault.ErrPacketMalformed, "stream", rec.Offset,
				"illegal xor header count=%d payloadLen=%d", count, payloadLen)
		}
		body := rec.Payload[xorHeaderSize:]
		if uint32(len(body)) != payloadLen {
			return nil, fault.New(fault.ErrPacketMalformed, "stream", rec.Offset,
				"xor body length %d != declared %d", len(body), payloadLen)
		}
		out = append(out, XORFrame{
			BaseSeq:    uint16(base),
			Count:      count,
			PayloadLen: payloadLen,
			XOR:        append([]byte(nil), body...),
			Offset:     rec.Offset,
		})
	}
	return out, nil
}

// RecoveryResult counts what redundancy could and could not repair.
type RecoveryResult struct {
	Recovered   int
	Unrecovered int
	// UnrecoveredSeqs lists sequences still missing after recovery, in order.
	UnrecoveredSeqs []uint16
	// Frames lists the RTP payloads synthesized for recovered packets.
	Frames []Packet
}

// Recover applies every XOR frame to the ordered report. A frame repairs
// exactly one missing packet: with one hole, the missing payload is the XOR
// of the redundancy bytes with every received payload. Frames with zero or
// multiple holes cannot reconstruct anything uniquely and are counted as
// unrecovered rather than guessed.
func Recover(report *SequenceReport, frames []XORFrame) (*RecoveryResult, error) {
	res := &RecoveryResult{}
	bySeq := make(map[uint16]*Packet, len(report.Ordered))
	for _, p := range report.Ordered {
		bySeq[p.Sequence] = p
	}

	for _, frame := range frames {
		hole, holeCount := uint16(0), 0
		repairable := true
		for k := uint32(0); k < frame.Count; k++ {
			seq := frame.BaseSeq + uint16(k)
			p, ok := bySeq[seq]
			if !ok {
				// The frame references sequences never observed in the
				// stream: it cannot repair anything here, so skip it and
				// leave the missing packets to the unrecovered count.
				repairable = false
				break
			}
			if p.Offset < 0 && !p.Recovered {
				hole, holeCount = seq, holeCount+1
			} else if uint32(len(p.Payload)) != frame.PayloadLen {
				repairable = false
				break
			}
		}
		if !repairable || holeCount != 1 {
			continue // redundant frame, multiple holes or inapplicable frame
		}

		payload := append([]byte(nil), frame.XOR...)
		for k := uint32(0); k < frame.Count; k++ {
			seq := frame.BaseSeq + uint16(k)
			if seq == hole {
				continue
			}
			other := bySeq[seq].Payload
			for i := range payload {
				payload[i] ^= other[i]
			}
		}
		recovered := &Packet{
			Sequence:  hole,
			Payload:   payload,
			Offset:    frame.Offset, // attribution: the redundancy frame that rebuilt it
			Recovered: true,
		}
		bySeq[hole] = recovered
		for i, p := range report.Ordered {
			if p.Sequence == hole {
				report.Ordered[i] = recovered
				break
			}
		}
		res.Recovered++
		res.Frames = append(res.Frames, *recovered)
	}

	for _, p := range report.Ordered {
		if p.Offset < 0 && !p.Recovered {
			res.Unrecovered++
			res.UnrecoveredSeqs = append(res.UnrecoveredSeqs, p.Sequence)
		}
	}
	return res, nil
}
