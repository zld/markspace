package ts

import (
	"example.com/assetpack/internal/fault"
)

// Minimal transport-stream builders shared by the TS tests.

func pcrBytes(base uint64, ext uint16) []byte {
	return []byte{
		byte(base >> 25), byte(base >> 17), byte(base >> 9),
		byte(base >> 1), byte(base<<7) | byte(ext>>8), byte(ext),
	}
}

func adaptField(pcr *uint64, ext uint16, discontinuity, randomAccess bool) []byte {
	flags := byte(0)
	if discontinuity {
		flags |= AFDiscontinuity
	}
	if randomAccess {
		flags |= AFRandomAccess
	}
	if pcr != nil {
		flags |= AFPCR
		return append([]byte{7, flags}, pcrBytes(*pcr, ext)...)
	}
	return []byte{0, flags}
}

type pktSpec struct {
	pid     uint16
	cc      uint8
	pusi    bool
	pcr     *uint64
	pcrExt  uint16
	disc    bool
	payload []byte
	afOnly  bool
}

func buildPacket(s pktSpec) []byte {
	p := make([]byte, PacketSize)
	p[0] = SyncByte
	p[1] = byte(s.pid >> 8)
	if s.pusi {
		p[1] |= 0x40
	}
	p[2] = byte(s.pid)
	af := adaptField(s.pcr, s.pcrExt, s.disc, false)
	if s.afOnly {
		p[3] = 0x20 | s.cc
		af[0] = byte(PacketSize - 5)
		copy(p[4:], af)
		return p
	}
	p[3] = 0x30 | s.cc
	copy(p[4:], af)
	n := copy(p[4+len(af):], s.payload)
	for i := 4 + len(af) + n; i < PacketSize; i++ {
		p[i] = 0xFF
	}
	return p
}

func buildPacketNoAdapt(s pktSpec) []byte {
	p := make([]byte, PacketSize)
	p[0] = SyncByte
	p[1] = byte(s.pid >> 8)
	if s.pusi {
		p[1] |= 0x40
	}
	p[2] = byte(s.pid)
	p[3] = 0x10 | s.cc
	copy(p[4:], s.payload)
	for i := 4 + len(s.payload); i < PacketSize; i++ {
		p[i] = 0xFF
	}
	return p
}

func psiSection(tableID uint8, tableIDExt uint16, version uint8, body []byte) []byte {
	sec := make([]byte, 0, 3+5+len(body)+4)
	sec = append(sec, tableID)
	length := 5 + len(body) + 4
	sec = append(sec, byte(0xB0|length>>8), byte(length))
	sec = append(sec, byte(tableIDExt>>8), byte(tableIDExt))
	sec = append(sec, byte(0xC1|version<<1)) // current_next=1
	sec = append(sec, 0, 0)
	sec = append(sec, body...)
	crc := MPEGCRC32(sec)
	return append(sec, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

func patSection(version uint8, program uint16, pmtPID uint16) []byte {
	body := []byte{byte(program >> 8), byte(program), byte(0xE0 | pmtPID>>8), byte(pmtPID)}
	return psiSection(TableIDPAT, 0, version, body)
}

func pmtSection(version uint8, program, pcrPID, audioPID uint16) []byte {
	body := []byte{byte(0xE0 | pcrPID>>8), byte(pcrPID), 0xF0, 0x00}
	// One AAC stream with an ISO-639 descriptor "eng".
	es := []byte{StreamTypeAAC, byte(0xE0 | audioPID>>8), byte(audioPID), 0xF0, 0x05, 0x0A, 0x03, 'e', 'n', 'g'}
	body = append(body, es...)
	return psiSection(TableIDPMT, program, version, body)
}

func psiPacket(pid uint16, cc uint8, section []byte) []byte {
	payload := append([]byte{0}, section...)
	return buildPacketNoAdapt(pktSpec{pid: pid, cc: cc, pusi: true, payload: payload})
}

func pcrPtr(v uint64) *uint64 { return &v }

func findFault(l *fault.List, kind fault.Kind) *fault.Fault {
	for _, f := range l.Items {
		if f.Kind == kind {
			return f
		}
	}
	return nil
}
