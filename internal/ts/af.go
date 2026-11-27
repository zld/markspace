// Package ts: adaptation-field parsing.
package ts

import (
	"example.com/assetpack/internal/fault"
)

// adapt parses the adaptation field and returns the payload slice.
func (p *parser) adapt(pkt *Packet, raw []byte) ([]byte, bool) {
	body := raw[4:]
	var payload []byte
	if pkt.AFC&2 != 0 {
		if len(body) == 0 {
			p.stream.Faults.Add(fault.New(fault.StageParse, fault.KindTruncated,
				"adaptation field flag set but no bytes remain on PID %d", pkt.PID).
				AtOffset(pkt.Offset).Recover(true))
			return nil, false
		}
		afLen := int(body[0])
		if afLen+1 > len(body) {
			p.stream.Faults.Add(fault.New(fault.StageParse, fault.KindTruncated,
				"adaptation field length %d exceeds packet remainder %d on PID %d", afLen, len(body), pkt.PID).
				AtOffset(pkt.Offset).Recover(true))
			return nil, false
		}
		af := body[1 : 1+afLen]
		p.parseAF(pkt, af)
		body = body[1+afLen:]
	}
	if pkt.AFC&1 != 0 {
		payload = body
	}
	return payload, true
}

func (p *parser) parseAF(pkt *Packet, af []byte) {
	if len(af) == 0 {
		return
	}
	flags := af[0]
	pkt.Discontinuity = flags&AFDiscontinuity != 0
	pkt.RandomAccess = flags&AFRandomAccess != 0
	if flags&AFPCR == 0 {
		return
	}
	if len(af) < 7 {
		p.stream.Faults.Add(fault.New(fault.StageParse, fault.KindPCR,
			"PCR flagged but adaptation field is only %d bytes", len(af)).
			AtOffset(pkt.Offset).Recover(true))
		return
	}
	base := uint64(af[1])<<25 | uint64(af[2])<<17 | uint64(af[3])<<9 |
		uint64(af[4])<<1 | uint64(af[5])>>7
	ext := uint16(af[5]&0x01)<<8 | uint16(af[6])
	pkt.HasPCR = true
	pkt.PCRRaw = base
	pkt.PCRExtension = ext
}
