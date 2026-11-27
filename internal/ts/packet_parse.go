package ts

import (
	"tsrepair/internal/mediaerr"
)

// parsePacket decodes one 188-byte buffer whose sync byte has already been
// validated. Structural problems produce a structured error; a partially
// decoded packet may still be returned (with Raw set) so the caller can keep
// its offset accounting.
func parsePacket(b []byte, off int64) (*Packet, *mediaerr.Error) {
	p := &Packet{
		Offset:           off,
		AdaptationLength: -1,
		Raw:              b,
	}
	p.TEI = b[1]&0x80 != 0
	p.PUSI = b[1]&0x40 != 0
	p.Priority = b[1]&0x20 != 0
	p.PID = uint16(b[1]&0x1F)<<8 | uint16(b[2])
	p.AdaptationControl = (b[3] >> 4) & 0x03
	p.CC = b[3] & 0x0F

	var perr *mediaerr.Error
	if p.AdaptationControl == AFCReserved {
		perr = mediaerr.Fatal(mediaerr.CodeAdaptationOverflow, p.PID, off,
			"reserved adaptation_field_control value 00",
			"skip packet; do not treat payload as continuous data")
		return p, perr
	}

	pos := 4
	if p.AdaptationControl == AFCAdaptationOnly || p.AdaptationControl == AFCPayloadAndAF {
		if pos >= PacketSize {
			return p, mediaerr.Fatal(mediaerr.CodeAdaptationOverflow, p.PID, off,
				"adaptation field header truncated", "skip packet")
		}
		afLen := int(b[pos])
		p.AdaptationLength = afLen
		// adaptation_field_length counts the bytes after the length byte.
		if afLen+pos+1 > PacketSize {
			return p, mediaerr.Fatal(mediaerr.CodeAdaptationOverflow, p.PID, off,
				"adaptation field length exceeds packet bounds", "skip packet")
		}
		if afLen > 0 {
			flags := b[pos+1]
			// PCR flag: adaptation field must hold at least the 6 PCR bytes.
			if flags&0x10 != 0 {
				if afLen < 7 {
					return p, mediaerr.Fatal(mediaerr.CodeAdaptationOverflow, p.PID, off,
						"PCR flag set but adaptation field too short", "skip packet")
				}
				p.HasPCR = true
				p.PCRBase, p.PCRExt = decodePCR(b[pos+2 : pos+8])
			}
			// Random access indicator (H.264/H.265 keyframe marker).
			p.RandomAccess = flags&0x40 != 0
		}
		pos += afLen + 1
	}

	if p.AdaptationControl == AFCPayloadOnly || p.AdaptationControl == AFCPayloadAndAF {
		if pos > PacketSize {
			return p, mediaerr.Fatal(mediaerr.CodeAdaptationOverflow, p.PID, off,
				"payload start beyond packet end", "skip packet")
		}
		p.Payload = b[pos:]
	}
	if p.TEI {
		perr = mediaerr.Recoverable(mediaerr.CodeSyncLost, p.PID, off,
			"transport error indicator set by upstream",
			"parse headers but mark payload untrusted")
	}
	return p, perr
}

// decodePCR reads the 33-bit base and 9-bit extension from the 6-byte PCR
// field, returning base in 90 kHz units and ext in 1/27 MHz units.
func decodePCR(b []byte) (base int64, ext int64) {
	base = int64(b[0])<<25 | int64(b[1])<<17 | int64(b[2])<<9 |
		int64(b[3])<<1 | int64(b[4]>>7)
	ext = int64(b[4]&0x01)<<8 | int64(b[5])
	return base, ext
}

// checkCC enforces MPEG-TS continuity counter semantics for one PID. The CC
// is a 4-bit counter; packets carrying only an adaptation field do not
// increment it, and a duplicate CC is legal only when the payload is also a
// duplicate.
func (r *Reader) checkCC(p *Packet) {
	st, ok := r.cc[p.PID]
	if !ok {
		st = &pidState{}
		r.cc[p.PID] = st
	}
	defer func() {
		st.lastCC = p.CC
		st.haveCC = true
		st.lastPktNo++
	}()
	if !st.haveCC {
		return
	}
	// Adaptation-only packets are permitted to repeat the previous CC.
	if p.AdaptationControl == AFCAdaptationOnly {
		return
	}
	delta := int(p.CC) - int(st.lastCC)
	if delta < 0 {
		delta += 16
	}
	switch {
	case delta == 0:
		r.ec.Add(mediaerr.Recoverable(mediaerr.CodeCCRepeat, p.PID, p.Offset,
			"continuity counter repeated",
			"treat as duplicate packet; drop repeated payload"))
	case delta == 1:
		// Continuous.
	default:
		// 2..15: either a gap (missing packets) or a large discontinuity jump.
		if delta > 8 {
			r.ec.Add(mediaerr.Recoverable(mediaerr.CodeCCJump, p.PID, p.Offset,
				"continuity counter discontinuity",
				"flush partial section; resume from this packet"))
		} else {
			r.ec.Add(mediaerr.Recoverable(mediaerr.CodeCCGap, p.PID, p.Offset,
				"continuity counter gap: missing packets",
				"flush partial section; resume from this packet"))
		}
	}
}
