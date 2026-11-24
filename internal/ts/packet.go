package ts

import (
	"fmt"

	"medpipe/internal/fault"
)

const (
	// PacketSize is the fixed MPEG-TS packet size.
	PacketSize = 188
	// SyncByte marks every TS packet.
	SyncByte = 0x47
	// PIDNull is the null-packet PID.
	PIDNull = 0x1fff
	// MaxInputBytes bounds a single accepted input.
	MaxInputBytes = 64 << 20
)

// AdaptationField contains the validated adaptation metadata needed for PCR.
type AdaptationField struct {
	Length              int
	Discontinuity       bool
	RandomAccess        bool
	ElementaryStreamPri bool
	HasPCR              bool
	PCRBase             uint64
	PCRExtension        uint16
	PCRFull             uint64
}

// Packet is a validated 188-byte MPEG-TS packet.
type Packet struct {
	Raw               []byte
	Offset            int64
	TransportError    bool
	PayloadUnitStart  bool
	Priority          bool
	PID               uint16
	ScramblingControl uint8
	AdaptationControl uint8
	ContinuityCounter uint8
	Adaptation        *AdaptationField
	Payload           []byte
}

// HasAdaptation reports whether the AFC includes an adaptation field.
func (p *Packet) HasAdaptation() bool {
	return p.AdaptationControl == 2 || p.AdaptationControl == 3
}

// HasPayload reports whether the AFC includes payload bytes.
func (p *Packet) HasPayload() bool {
	return p.AdaptationControl == 1 || p.AdaptationControl == 3
}

// ParsePacket validates one fixed-size TS packet.
func ParsePacket(raw []byte, offset int64) (*Packet, *fault.Error) {
	if len(raw) != PacketSize {
		return nil, fault.New(fault.ErrPacketLength, "ts.packet", offset,
			fmt.Sprintf("expected %d bytes, got %d", PacketSize, len(raw)))
	}
	if raw[0] != SyncByte {
		return nil, fault.New(fault.ErrSyncByte, "ts.packet", offset,
			fmt.Sprintf("expected sync 0x%02x, got 0x%02x", SyncByte, raw[0]))
	}

	pid := uint16(raw[1]&0x1f)<<8 | uint16(raw[2])
	if pid > PIDNull {
		return nil, fault.New(fault.ErrPID, "ts.packet", offset,
			fmt.Sprintf("PID %d outside 13-bit range", pid)).WithPID(pid)
	}
	afc := raw[3] >> 4 & 0x03
	if afc == 0 {
		return nil, fault.New(fault.ErrAdaptation, "ts.packet", offset,
			"adaptation_field_control=0 is reserved").WithPID(pid)
	}

	p := &Packet{
		Raw:               raw,
		Offset:            offset,
		TransportError:    raw[1]&0x80 != 0,
		PayloadUnitStart:  raw[1]&0x40 != 0,
		Priority:          raw[1]&0x20 != 0,
		PID:               pid,
		ScramblingControl: raw[3] >> 6,
		AdaptationControl: afc,
		ContinuityCounter: raw[3] & 0x0f,
	}

	pos := 4
	if p.HasAdaptation() {
		if pos >= PacketSize {
			return nil, fault.New(fault.ErrAdaptation, "ts.packet", offset,
				"missing adaptation length").WithPID(pid)
		}
		l := int(raw[pos])
		if pos+1+l > PacketSize {
			return nil, fault.New(fault.ErrAdaptation, "ts.packet", offset,
				fmt.Sprintf("adaptation length %d exceeds packet", l)).WithPID(pid)
		}
		af, fe := parseAdaptation(raw[pos:pos+1+l+1], offset, pid)
		if fe != nil {
			return nil, fe
		}
		p.Adaptation = af
		pos += 1 + l
	}
	if p.HasPayload() {
		p.Payload = raw[pos:]
	}
	return p, nil
}

func parseAdaptation(b []byte, offset int64, pid uint16) (*AdaptationField, *fault.Error) {
	af := &AdaptationField{Length: int(b[0])}
	if af.Length == 0 {
		return af, nil
	}
	if len(b) < 2 || af.Length+1 != len(b) {
		return nil, fault.New(fault.ErrAdaptation, "ts.adaptation", offset,
			"adaptation field length mismatch").WithPID(pid)
	}
	flags := b[1]
	af.Discontinuity = flags&0x80 != 0
	af.RandomAccess = flags&0x40 != 0
	af.ElementaryStreamPri = flags&0x20 != 0
	af.HasPCR = flags&0x10 != 0
	if af.HasPCR {
		// flags byte plus six PCR bytes must be present.
		if len(b) < 8 {
			return nil, fault.New(fault.ErrAdaptation, "ts.pcr", offset,
				"PCR flag set but PCR is truncated").WithPID(pid)
		}
		pcr := b[2:8]
		af.PCRBase = uint64(pcr[0])<<25 |
			uint64(pcr[1])<<17 |
			uint64(pcr[2])<<9 |
			uint64(pcr[3])<<1 |
			uint64(pcr[4])>>7
		af.PCRExtension = uint16(pcr[4]&0x01)<<8 | uint16(pcr[5])
		af.PCRFull = af.PCRBase*300 + uint64(af.PCRExtension)
	}
	return af, nil
}

// ContinuityMonitor tracks CC per PID and emits non-silent gap events.
type ContinuityMonitor struct {
	last map[uint16]uint8
	has  map[uint16]bool
}

// NewContinuityMonitor creates an empty CC monitor.
func NewContinuityMonitor() *ContinuityMonitor {
	return &ContinuityMonitor{last: map[uint16]uint8{}, has: map[uint16]bool{}}
}

// Observe checks one packet. A repeated CC is treated as a duplicate; a
// forward delta other than one reports the number of missing packets.
func (m *ContinuityMonitor) Observe(p *Packet) []fault.Event {
	if p == nil || p.PID == PIDNull {
		return nil
	}
	if !m.has[p.PID] {
		m.has[p.PID] = true
		m.last[p.PID] = p.ContinuityCounter
		return nil
	}
	last := m.last[p.PID]
	if p.ContinuityCounter == last {
		// Duplicate TS packet. RTP deduplication has usually already removed
		// it, but retaining the CC is still valid.
		return nil
	}
	delta := (p.ContinuityCounter - last) & 0x0f
	m.last[p.PID] = p.ContinuityCounter
	if delta == 1 {
		return nil
	}
	missing := int(delta) - 1
	if delta == 0 {
		missing = 15
	}
	return []fault.Event{{
		Code:      fault.ErrContinuity,
		Severity:  "gap",
		Stage:     "ts.continuity",
		PID:       p.PID,
		Offset:    p.Offset,
		EndOffset: p.Offset + PacketSize,
		Message:   fmt.Sprintf("CC jumped from %d to %d; %d packet(s) missing", last, p.ContinuityCounter, missing),
	}}
}
