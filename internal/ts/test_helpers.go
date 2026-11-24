package ts

// BuildPacket assembles a syntactically valid 188-byte TS packet for tests
// and the deterministic demo fixtures.
type BuildOpts struct {
	PID               uint16
	PUSI              bool
	AdaptationControl byte // 1=payload only, 2=adaptation only, 3=both
	CC                byte
	PCRBase           uint64
	PCRExt            uint16
	WithPCR           bool
	RandomAccess      bool
	Discontinuity     bool
	Payload           []byte // copied and 0xFF stuffed to fill the packet
}

// BuildPacket returns a freshly allocated packet buffer.
func BuildPacket(o BuildOpts) []byte {
	buf := make([]byte, PacketSize)
	for i := range buf {
		buf[i] = 0xFF
	}
	buf[0] = SyncByte
	buf[1] = byte(o.PID >> 8)
	buf[2] = byte(o.PID)
	if o.PUSI {
		buf[1] |= flagPUSI
	}
	if o.AdaptationControl == 0 {
		o.AdaptationControl = 1
	}
	buf[3] = (o.AdaptationControl << afcShift) | (o.CC & ccMask)
	pos := 4
	if o.AdaptationControl&0x02 != 0 {
		// First measure payload length to size the adaptation field.
		payloadLen := 0
		if o.AdaptationControl&0x01 != 0 {
			payloadLen = len(o.Payload)
		}
		// Need at least flags(1) and, with PCR, the 6-byte PCR field.
		minBody := 1
		if o.WithPCR {
			minBody = 8
		}
		// adaptation length byte itself occupies 1; total consumed = 1 + afLen.
		afLen := PacketSize - pos - payloadLen - 1
		if afLen < minBody {
			afLen = minBody
		}
		buf[pos] = byte(afLen)
		flags := byte(0)
		if o.WithPCR {
			flags |= adaptFlagPCR
		}
		if o.RandomAccess {
			flags |= adaptFlagRandom
		}
		if o.Discontinuity {
			flags |= adaptFlagDisc
		}
		if afLen >= 2 {
			buf[pos+1] = flags
			for k := pos + 2; k < pos+1+afLen; k++ {
				buf[k] = 0xFF
			}
		}
		if o.WithPCR && afLen >= 8 {
			b := buf[pos+2 : pos+8] // 6 PCR bytes; parser excludes the trailing reserved byte
			b[0] = byte(o.PCRBase >> 25)
			b[1] = byte(o.PCRBase >> 17)
			b[2] = byte(o.PCRBase >> 9)
			b[3] = byte(o.PCRBase >> 1)
			b[4] = byte(o.PCRBase<<7) | byte(o.PCRExt>>8&0x01)
			b[5] = byte(o.PCRExt)
		}
		pos += 1 + afLen
	}
	if o.AdaptationControl&0x01 != 0 && len(o.Payload) > 0 {
		copy(buf[pos:], o.Payload)
	}
	return buf
}
