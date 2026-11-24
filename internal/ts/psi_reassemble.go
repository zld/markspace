package ts

import (
	"fmt"

	"medpipe/internal/fault"
)

// SectionAssembler reassembles PSI sections from TS packet payloads. It is
// deliberately bounded: PSI is small and medpipe does not accept unbounded
// table growth.
type SectionAssembler struct {
	buffers map[uint16][]byte
	max     int
}

// NewSectionAssembler creates a PSI reassembler.
func NewSectionAssembler() *SectionAssembler {
	return &SectionAssembler{buffers: map[uint16][]byte{}, max: 64 * 1024}
}

// Feed appends one packet and returns any complete raw sections.
func (a *SectionAssembler) Feed(p *Packet) ([][]byte, *fault.Error) {
	if p == nil || !p.HasPayload() || p.PID == PIDNull {
		return nil, nil
	}
	payload := p.Payload
	if p.PayloadUnitStart {
		if len(payload) < 1 {
			return nil, fault.New(fault.ErrPSISection, "psi.reassemble", p.Offset, "PUSI packet missing pointer field").WithPID(p.PID)
		}
		pointer := int(payload[0])
		start := 1 + pointer
		if start > len(payload) {
			return nil, fault.New(fault.ErrPSISection, "psi.reassemble", p.Offset,
				fmt.Sprintf("pointer_field %d exceeds payload %d", pointer, len(payload)-1)).WithPID(p.PID)
		}
		// Generated and common PAT/PMT sections use pointer=0. Drop any
		// incomplete stale section rather than letting version state mix.
		a.buffers[p.PID] = append([]byte(nil), payload[start:]...)
	} else {
		if len(a.buffers[p.PID]) == 0 {
			return nil, nil // fragment without section start
		}
		a.buffers[p.PID] = append(a.buffers[p.PID], payload...)
	}
	if len(a.buffers[p.PID]) > a.max {
		return nil, fault.New(fault.ErrInputTooLarge, "psi.reassemble", p.Offset, "PSI section buffer exceeds limit").WithPID(p.PID)
	}

	var sections [][]byte
	for {
		b := a.buffers[p.PID]
		if len(b) < 3 {
			break
		}
		sectionLen := int(uint16(b[1]&0x0f)<<8 | uint16(b[2]))
		total := sectionLen + 3
		if sectionLen < 9 || total > a.max {
			return sections, fault.New(fault.ErrPSISection, "psi.reassemble", p.Offset, "invalid PSI section length").WithPID(p.PID)
		}
		if len(b) < total {
			break
		}
		section := append([]byte(nil), b[:total]...)
		sections = append(sections, section)
		a.buffers[p.PID] = append([]byte(nil), b[total:]...)
		if len(a.buffers[p.PID]) == 0 {
			break
		}
	}
	return sections, nil
}

// HandlePacket feeds one TS packet through reassembly and then the versioned
// section tracker.
func (p *PSIParser) HandlePacket(a *SectionAssembler, pkt *Packet) (committed []bool, err *fault.Error) {
	sections, ferr := a.Feed(pkt)
	if ferr != nil {
		p.events = append(p.events, fault.Event{Code: ferr.Code, Severity: "corrupt", Stage: ferr.Stage, PID: pkt.PID, Offset: ferr.Offset, Message: ferr.Message})
		return nil, ferr
	}
	for _, section := range sections {
		ok, serr := p.SubmitSection(pkt.PID, section)
		committed = append(committed, ok)
		if serr != nil {
			return committed, serr
		}
	}
	return committed, nil
}
