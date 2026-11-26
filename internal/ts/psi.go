package ts

import (
	"encoding/binary"
	"fmt"

	"liveregress/internal/fault"
)

// SectionAssembler handles the pointer_field and section continuation rules.
type SectionAssembler struct {
	path   string
	pid    uint16
	cur    []byte
	start  int64
	want   int // total section length once known
	active bool
}

// NewSectionAssembler builds an assembler for pid.
func NewSectionAssembler(path string, pid uint16) *SectionAssembler {
	return &SectionAssembler{path: path, pid: pid}
}

// Push consumes one packet payload and returns any completed section.
// Multiple sections in one payload are returned one at a time.
func (a *SectionAssembler) Push(p *Packet) ([][]byte, error) {
	if !p.HasPayload || len(p.Payload) == 0 {
		return nil, nil
	}
	data := p.Payload
	off := p.PayloadOffset
	if p.PUSI {
		ptr := int(data[0])
		if 1+ptr > len(data) {
			return nil, fault.New(fault.KindCorrupt, a.path, p.Offset,
				fmt.Sprintf("PSI pointer_field %d overruns payload on pid %d", ptr, a.pid)).Recover(false)
		}
		a.cur = nil
		a.active = false
		data = data[1+ptr:]
		off += int64(1 + ptr)
		a.start = off
	} else if !a.active {
		// Continuation without a start is benign filler before the first
		// section only when there is no pending section.
		return nil, nil
	}
	var out [][]byte
	for len(data) > 0 {
		if !a.active {
			if len(data) < 3 {
				return nil, nil // stuffing; a new section begins next packet
			}
			if data[0] == 0xFF {
				return out, nil // stuffing bytes
			}
			secLen := int(binary.BigEndian.Uint16(data[1:3]) & 0x0FFF)
			if secLen < 4 || secLen > 1021 {
				return nil, fault.New(fault.KindCorrupt, a.path, a.start,
					fmt.Sprintf("invalid PSI section length %d on pid %d", secLen, a.pid)).Recover(false)
			}
			// section_length counts bytes after the 3-byte section header, so
			// the assembled section keeps the header and totals secLen+3.
			a.want = secLen + 3
			a.active = true
			a.cur = append(a.cur[:0], data[:3]...)
			data = data[3:]
			off += 3
		}
		need := a.want - len(a.cur)
		take := need
		if take > len(data) {
			take = len(data)
		}
		a.cur = append(a.cur, data[:take]...)
		data = data[take:]
		off += int64(take)
		if len(a.cur) == a.want {
			sec := a.cur
			a.cur = nil
			a.active = false
			out = append(out, sec)
		}
	}
	return out, nil
}

// PATEntry maps a program number to its PMT PID.
type PATEntry struct {
	Program uint16
	PID     uint16
}

// ParsePAT decodes a program association section.
func ParsePAT(sec []byte, path string, off int64) ([]PATEntry, error) {
	if len(sec) < 8 || sec[0] != 0x00 {
		return nil, fault.New(fault.KindCorrupt, path, off, "not a PAT section").Recover(false)
	}
	if sec[5]&0x01 == 0 {
		return nil, fault.New(fault.KindCorrupt, path, off, "PAT section not complete yet").Recover(false)
	}
	body := sec[8 : len(sec)-4]
	if len(body)%4 != 0 {
		return nil, fault.New(fault.KindCorrupt, path, off, "PAT body is not a multiple of 4").Recover(false)
	}
	var out []PATEntry
	for i := 0; i+4 <= len(body); i += 4 {
		prog := binary.BigEndian.Uint16(body[i : i+2])
		pid := binary.BigEndian.Uint16(body[i+2:i+4]) & 0x1FFF
		if prog == 0 {
			continue // network PID
		}
		out = append(out, PATEntry{Program: prog, PID: pid})
	}
	return out, nil
}

// ESInfo is one elementary stream declared in a PMT.
type ESInfo struct {
	PID        uint16
	StreamType uint8
	Language   string
}

// PMT is a parsed program map table.
type PMT struct {
	Program           uint16
	PCRPID            uint16
	ProgramInfoLength int
	Streams           []ESInfo
}

// Stream type constants used by fixtures and tests.
const (
	StreamTypeH264 uint8 = 0x1B
	StreamTypeH265 uint8 = 0x24
	StreamTypeAAC  uint8 = 0x0F
	StreamTypeMP3  uint8 = 0x03
	StreamTypeAC3  uint8 = 0x81
)

// ParsePMT decodes a program map section.
func ParsePMT(sec []byte, path string, off int64) (*PMT, error) {
	if len(sec) < 12 || sec[0] != 0x02 {
		return nil, fault.New(fault.KindCorrupt, path, off, "not a PMT section").Recover(false)
	}
	if sec[5]&0x01 == 0 {
		return nil, fault.New(fault.KindCorrupt, path, off, "PMT section not complete yet").Recover(false)
	}
	end := len(sec) - 4 // strip CRC32
	if end < 12 {
		return nil, fault.New(fault.KindCorrupt, path, off, "PMT too short").Recover(false)
	}
	pmt := &PMT{
		Program: binary.BigEndian.Uint16(sec[3:5]),
		PCRPID:  binary.BigEndian.Uint16(sec[8:10]) & 0x1FFF,
	}
	pil := int(binary.BigEndian.Uint16(sec[10:12]) & 0x0FFF)
	pmt.ProgramInfoLength = pil
	pos := 12 + pil
	if pos > end {
		return nil, fault.New(fault.KindCorrupt, path, off, "PMT program_info_length overruns section").Recover(false)
	}
	for pos+5 <= end {
		st := sec[pos]
		pid := binary.BigEndian.Uint16(sec[pos+1:pos+3]) & 0x1FFF
		esil := int(binary.BigEndian.Uint16(sec[pos+3:pos+5]) & 0x0FFF)
		pos += 5
		if pos+esil > end {
			return nil, fault.New(fault.KindCorrupt, path, off, "PMT ES_info_length overruns section").Recover(false)
		}
		lang := ""
		if esil >= 2 {
			lang = string(sec[pos : pos+2])
		}
		pmt.Streams = append(pmt.Streams, ESInfo{PID: pid, StreamType: st, Language: lang})
		pos += esil
	}
	if len(pmt.Streams) == 0 {
		return nil, fault.New(fault.KindMissing, path, off, "PMT declares no elementary streams").Recover(false)
	}
	return pmt, nil
}
