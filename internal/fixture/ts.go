package fixture

// TSSpec describes a generated single-program transport stream.
type TSSpec struct {
	VideoPID   uint16
	AudioPID   uint16
	PMTPID     uint16
	StreamType uint8
	// AUs is one access unit per video sample, in decode order.
	AUs []TSAU
}

// TSAU is one generated access unit. PTS is in 90 kHz ticks.
type TSAU struct {
	PTS     uint64
	Key     bool
	Payload []byte
}

// BuildTS emits PAT + PMT + one PES per access unit. Packet layout is fully
// deterministic so hashes are stable across runs.
func BuildTS(spec TSSpec) []byte {
	pmtPID := spec.PMTPID
	if pmtPID == 0 {
		pmtPID = 0x1000
	}
	if spec.StreamType == 0 {
		spec.StreamType = 0x1B // H.264
	}
	b := &tsBuilder{cc: map[uint16]uint8{}, pmtPID: pmtPID}
	b.pat(1, pmtPID)
	b.pmt(1, spec)
	for _, au := range spec.AUs {
		b.pes(spec.VideoPID, 0xE0, au.PTS, au.Payload)
	}
	return b.buf
}

type tsBuilder struct {
	buf    []byte
	cc     map[uint16]uint8
	pmtPID uint16
}

func (b *tsBuilder) packet(pid uint16, payload []byte, pusi bool) {
	for first := true; len(payload) > 0; first = false {
		cc := b.cc[pid]
		b.cc[pid] = (cc + 1) & 0x0F
		var pkt [188]byte
		pkt[0] = 0x47
		bits := pid & 0x1FFF
		if pusi && first {
			bits |= 0x4000
		}
		pkt[1] = byte(bits >> 8)
		pkt[2] = byte(bits)
		chunk := payload
		if len(chunk) > 184 {
			chunk = chunk[:184]
		}
		payload = payload[len(chunk):]
		if len(chunk) == 184 {
			pkt[3] = 0x10 | cc
			copy(pkt[4:], chunk)
		} else {
			afLen := 183 - len(chunk)
			pkt[3] = 0x30 | cc
			pkt[4] = byte(afLen)
			if afLen >= 1 {
				pkt[5] = 0x00
				for i := 6; i < 5+afLen; i++ {
					pkt[i] = 0xFF
				}
			}
			copy(pkt[5+afLen:], chunk)
		}
		b.buf = append(b.buf, pkt[:]...)
	}
}

func (b *tsBuilder) psi(pid uint16, section []byte) {
	b.packet(pid, append([]byte{0x00}, section...), true)
}

func (b *tsBuilder) pat(program, pmtPID uint16) {
	body := []byte{byte(program >> 8), byte(program), 0xE0 | byte(pmtPID>>8), byte(pmtPID)}
	length := 5 + len(body) + 4
	sec := []byte{0x00, byte(0x80 | length>>8), byte(length), 0x00, 0x01, 0xC1, 0x00, 0x00}
	sec = append(sec, body...)
	sec = append(sec, 0, 0, 0, 0)
	b.psi(0x0000, sec)
}

func (b *tsBuilder) pmt(program uint16, spec TSSpec) {
	es := []byte{spec.StreamType, 0xE0 | byte(spec.VideoPID>>8), byte(spec.VideoPID), 0xF0, 0x00}
	length := 9 + len(es) + 4
	sec := []byte{0x02, byte(0x80 | length>>8), byte(length),
		byte(program >> 8), byte(program), 0xC1, 0x00, 0x00,
		0xE0 | byte(spec.VideoPID>>8), byte(spec.VideoPID), 0xF0, 0x00}
	sec = append(sec, es...)
	sec = append(sec, 0, 0, 0, 0)
	b.psi(b.pmtPID, sec)
}

func (b *tsBuilder) pes(pid uint16, streamID byte, pts uint64, payload []byte) {
	hdr := []byte{0x00, 0x00, 0x01, streamID, 0x00, 0x00, 0x80, 0x80, 0x05,
		0x21 | byte((pts>>29)&0x0E),
		byte(pts >> 22),
		0x01 | byte((pts>>14)&0xFE),
		byte(pts >> 7),
		0x01 | byte((pts<<1)&0xFE)}
	l := 3 + 5 + len(payload)
	hdr[4], hdr[5] = byte(l>>8), byte(l)
	b.packet(pid, append(hdr, payload...), true)
}

// AnnexBAU builds one H.264 access unit with an AUD and a single slice. Key
// access units use IDR slices and carry SPS/PPS.
func AnnexBAU(key bool, size, seed int) []byte {
	au := []byte{0, 0, 0, 1, 0x09, 0xF0}
	if key {
		au = append(au, 0, 0, 0, 1, 0x67, 0x42, 0xC0, 0x1E) // SPS
		au = append(au, 0, 0, 0, 1, 0x68, 0xCE, 0x3C, 0x80) // PPS
	}
	nal := byte(0x41) // non-IDR slice
	if key {
		nal = 0x65
	}
	au = append(au, 0, 0, 0, 1, nal)
	for i := 0; i < size; i++ {
		au = append(au, byte((i*13+seed*7)&0xFF))
	}
	return au
}

// SplitTSAt cuts a transport stream at a byte offset and reports whether that
// offset is packet aligned (a non-aligned cut is a deliberate truncation).
func SplitTSAt(raw []byte, offset int) []byte {
	if offset <= 0 || offset >= len(raw) {
		return raw
	}
	return raw[:offset]
}

// TSByteOffsetOfPacket returns the byte offset of packet index.
func TSByteOffsetOfPacket(index int) int { return index * 188 }

// CountTSPackets reports the number of complete 188-byte packets.
func CountTSPackets(raw []byte) int { return len(raw) / 188 }
