// Package ts: PSI section reassembly across transport packets.
package ts

import (
	"example.com/assetpack/internal/fault"
)

func (p *psiParser) feed(pkt *Packet, idx int) {
	buf := p.partial[pkt.PID]
	if pkt.PUSI {
		payload := pkt.Payload
		pointer := int(payload[0])
		if 1+pointer > len(payload) {
			p.res.Faults.Add(fault.New(fault.StageParse, fault.KindPSIInvalid,
				"pointer_field %d exceeds payload %d on PID %d", pointer, len(payload), pkt.PID).
				AtOffset(pkt.Offset).Recover(true))
			delete(p.partial, pkt.PID)
			return
		}
		// Bytes before the pointer complete any section already in flight.
		if buf != nil {
			buf.data = append(buf.data, payload[1:1+pointer]...)
			p.drain(buf, pkt)
		}
		buf = &sectionBuf{pid: pkt.PID, startOff: pkt.Offset, startPkt: idx, span: 1}
		buf.data = append(buf.data, payload[1+pointer:]...)
		p.partial[pkt.PID] = buf
	} else {
		if buf == nil {
			return
		}
		buf.data = append(buf.data, pkt.Payload...)
		buf.span++
	}
	p.drain(buf, pkt)
}

func (p *psiParser) drain(buf *sectionBuf, pkt *Packet) {
	for len(buf.data) >= 3 {
		if buf.data[0] == 0xFF {
			buf.data = buf.data[1:]
			continue
		}
		secLen := int(buf.data[1]&0x0F)<<8 | int(buf.data[2])
		total := 3 + secLen
		if secLen == 0 || total > 4096 {
			p.res.Faults.Add(fault.New(fault.StageParse, fault.KindPSIInvalid,
				"implausible section_length %d on PID %d", secLen, buf.pid).
				AtOffset(pkt.Offset).Recover(true))
			buf.data = nil
			delete(p.partial, buf.pid)
			return
		}
		if len(buf.data) < total {
			return
		}
		sec := make([]byte, total)
		copy(sec, buf.data[:total])
		buf.data = buf.data[total:]
		if buf.span > 1 {
			p.res.Faults.Add(fault.New(fault.StageParse, fault.KindPSIInvalid,
				"section on PID %d spans %d transport packets", buf.pid, buf.span).
				AtOffset(buf.startOff).AtSample(int64(buf.span)).Recover(true))
		}
		buf.span = 1
		if MPEGCRC32(sec) != 0 {
			p.res.Faults.Add(fault.New(fault.StageParse, fault.KindPSIChecksum,
				"PSI CRC mismatch on PID %d; section discarded", buf.pid).
				AtOffset(buf.startOff).Recover(true))
			continue
		}
		p.dispatchParsed(pkt, buf.startPkt, buf.startOff, sec)
	}
	if len(buf.data) == 0 {
		delete(p.partial, buf.pid)
	}
}

// firstSectionTableID peeks at the section that begins in a PUSI packet,
// after skipping any bytes that finish the previous section.
func firstSectionTableID(payload []byte) (uint8, bool) {
	if len(payload) == 0 {
		return 0, false
	}
	pointer := int(payload[0])
	if 1+pointer >= len(payload) {
		return 0, false
	}
	return payload[1+pointer], true
}

// dispatchParsed is supplied in full by psi_tables.go; this placeholder keeps
// section reassembly independently compilable.
func (p *psiParser) dispatchParsed(pkt *Packet, idx int, off int64, sec []byte) {}
