package ts

import (
	"fmt"

	"medpipe/internal/fault"
)

// PESPacket is a reassembled Packetized Elementary Stream packet.
type PESPacket struct {
	PID      uint16
	StreamID uint8
	PTS      uint64
	HasPTS   bool
	DTS      uint64
	HasDTS   bool
	Data     []byte
	Offset   int64
}

type pesBuffer struct {
	data     []byte
	startOff int64
	started  bool
}

// PESAssembler reassembles PES packets for elementary PIDs.
type PESAssembler struct {
	buffers map[uint16]*pesBuffer
	maxPES  int
}

// NewPESAssembler creates a bounded PES reassembler.
func NewPESAssembler() *PESAssembler {
	return &PESAssembler{buffers: map[uint16]*pesBuffer{}, maxPES: MaxInputBytes}
}

// Feed handles one TS packet and returns completed PES packets.
func (a *PESAssembler) Feed(p *Packet) ([]*PESPacket, []fault.Event, *fault.Error) {
	if p == nil || !p.HasPayload() {
		return nil, nil, nil
	}
	b := a.buffers[p.PID]
	if b == nil {
		b = &pesBuffer{}
		a.buffers[p.PID] = b
	}
	var events []fault.Event
	var completed []*PESPacket

	if p.PayloadUnitStart {
		if b.started && len(b.data) > 0 {
			// A new PES start before the old declared/end-of-stream PES was
			// completed means the prior sample is truncated.
			events = append(events, fault.Event{
				Code: fault.ErrCorruptSample, Severity: "gap", Stage: "ts.pes",
				PID: p.PID, Offset: b.startOff, EndOffset: p.Offset,
				Message: "PES packet truncated before next payload-unit start",
			})
		}
		b.data = append([]byte(nil), p.Payload...)
		b.started = true
		b.startOff = p.Offset
	} else if b.started {
		b.data = append(b.data, p.Payload...)
	} else {
		return nil, nil, nil
	}
	if len(b.data) > a.maxPES {
		return nil, events, fault.New(fault.ErrInputTooLarge, "ts.pes", p.Offset, "PES buffer exceeds input limit").WithPID(p.PID)
	}

	pes, need, perr := parsePES(b.data, p.PID, b.startOff)
	if perr != nil {
		// Header may be incomplete until more fragments arrive.
		if len(b.data) < 9 {
			return nil, events, nil
		}
		return nil, events, perr
	}
	if need < 0 {
		// Unbounded video PES: wait for the next payload-unit start or
		// Finish; do not emit a partial sample.
		return completed, events, nil
	}
	if need > 0 && len(b.data) >= need {
		pes, _, perr = parsePES(b.data[:need], p.PID, b.startOff)
		if perr != nil {
			return nil, events, perr
		}
		completed = append(completed, pes)
		rem := append([]byte(nil), b.data[need:]...)
		b.data = rem
		if len(rem) == 0 {
			b.started = false
		}
	}
	return completed, events, nil
}

// Finish returns an unbounded final PES if present. A short incomplete buffer
// is reported as truncation.
func (a *PESAssembler) Finish() ([]*PESPacket, []fault.Event) {
	var out []*PESPacket
	var events []fault.Event
	for pid, b := range a.buffers {
		if !b.started || len(b.data) == 0 {
			continue
		}
		pes, total, err := parsePES(b.data, pid, b.startOff)
		if err != nil || (total > 0 && len(b.data) < total) || len(pes.Data) == 0 {
			events = append(events, fault.Event{
				Code: fault.ErrTruncated, Severity: "fatal", Stage: "ts.pes",
				PID: pid, Offset: b.startOff, EndOffset: b.startOff + int64(len(b.data)),
				Message: "final PES/sample is truncated",
			})
			continue
		}
		out = append(out, pes)
	}
	return out, events
}

func parsePES(b []byte, pid uint16, offset int64) (*PESPacket, int, *fault.Error) {
	if len(b) < 6 {
		return nil, 0, fault.New(fault.ErrPacketLength, "ts.pes", offset, "PES header shorter than 6 bytes").WithPID(pid)
	}
	if b[0] != 0 || b[1] != 0 || b[2] != 1 {
		return nil, 0, fault.New(fault.ErrCorruptSample, "ts.pes", offset, "invalid PES start code").WithPID(pid)
	}
	streamID := b[3]
	packetLen := int(uint16(b[4])<<8 | uint16(b[5]))
	total := 0
	if packetLen == 0 {
		total = -1 // unbounded PES; complete at next PUSI or Finish
	} else {
		total = 6 + packetLen
		if len(b) < total {
			return nil, total, nil // need more fragments
		}
	}
	if len(b) < 9 {
		return nil, total, nil
	}
	headerLen := int(b[8])
	payloadStart := 9 + headerLen
	if payloadStart > len(b) {
		return nil, total, fault.New(fault.ErrCorruptSample, "ts.pes", offset, "PES header_data_length exceeds packet").WithPID(pid)
	}
	p := &PESPacket{PID: pid, StreamID: streamID, Offset: offset, Data: append([]byte(nil), b[payloadStart:]...)}
	ptsFlags := b[7] & 0xc0
	if ptsFlags == 0x80 || ptsFlags == 0xc0 {
		if payloadStart < 14 || len(b) < 14 {
			return nil, total, fault.New(fault.ErrCorruptSample, "ts.pes", offset, "PES PTS missing").WithPID(pid)
		}
		pts := b[9:14]
		p.PTS = uint64(pts[0]&0x0e)<<29 |
			uint64(pts[1])<<22 |
			uint64(pts[2]&0xfe)<<14 |
			uint64(pts[3])<<7 |
			uint64(pts[4])>>1
		p.HasPTS = true
	}
	if ptsFlags == 0xc0 && len(b) >= 19 {
		dts := b[14:19]
		p.DTS = uint64(dts[0]&0x0e)<<29 |
			uint64(dts[1])<<22 |
			uint64(dts[2]&0xfe)<<14 |
			uint64(dts[3])<<7 |
			uint64(dts[4])>>1
		p.HasDTS = true
	}
	if packetLen == 0 {
		// Unbounded PES: wait for an explicit boundary.
		p.Data = append([]byte(nil), b[payloadStart:]...)
		return p, -1, nil
	}
	if total > len(b) {
		return p, total, nil
	}
	p.Data = append([]byte(nil), b[payloadStart:total]...)
	return p, total, nil
}

// String helps tests and diagnostics.
func (p *PESPacket) String() string {
	return fmt.Sprintf("PES(pid=%d sid=0x%02x pts=%d bytes=%d)", p.PID, p.StreamID, p.PTS, len(p.Data))
}
