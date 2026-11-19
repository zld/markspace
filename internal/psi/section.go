package psi

import (
	"errors"
	"fmt"
)

// MaxSectionLength bounds a single PSI section including its 3-byte header,
// as required by this tool's safety limits (4096 bytes total).
const MaxSectionLength = 4096

// StuffingByte terminates a sequence of PSI sections inside one payload.
const StuffingByte = 0xff

// Section is a complete, CRC-validated PSI section.
type Section struct {
	TableID       uint8
	SectionLength uint16 // as encoded in the header (bytes after the header)
	Syntax        bool   // section_syntax_indicator
	Private       bool
	Extension     uint16 // table_id_extension, e.g. TSID for PAT or program number for PMT
	Version       uint8
	CurrentNext   bool
	Number        uint8
	LastNumber    uint8
	Body          []byte // table data after the 8-byte syntax header, excluding CRC
	Raw           []byte // complete section bytes including the trailing CRC
}

// TotalLength returns the encoded section size including header and CRC.
func (s Section) TotalLength() int { return 3 + int(s.SectionLength) }

// DiscardReason explains why an in-progress section was abandoned.
type DiscardReason string

const (
	DiscardContinuity DiscardReason = "continuity"
	DiscardTransport  DiscardReason = "transport_error"
	DiscardCRC        DiscardReason = "crc"
	DiscardLength     DiscardReason = "length"
	DiscardPointer    DiscardReason = "pointer"
	DiscardEOF        DiscardReason = "end_of_stream"
)

// ErrInvalidSection reports malformed section framing.
var ErrInvalidSection = errors.New("psi: invalid section")

// AssemblerStats is a snapshot of reassembly counters.
type AssemblerStats struct {
	Completed    int
	CRCErrors    int
	LengthErrors int
	Discarded    int
}

// Assembler reassembles cross-packet PSI sections for a single PID.
type Assembler struct {
	buf      []byte
	active   bool
	stats    AssemblerStats
	discards map[DiscardReason]int
}

// NewAssembler creates an empty assembler.
func NewAssembler() *Assembler {
	return &Assembler{discards: make(map[DiscardReason]int)}
}

// Discard abandons any in-progress section, e.g. after packet loss.
func (a *Assembler) Discard(r DiscardReason) {
	if a.active {
		a.stats.Discarded++
		a.discards[r]++
	}
	a.buf = a.buf[:0]
	a.active = false
}

// Discards returns a copy of the discard counters by reason.
func (a *Assembler) Discards() map[DiscardReason]int {
	out := make(map[DiscardReason]int, len(a.discards))
	for k, v := range a.discards {
		out[k] = v
	}
	return out
}

// Stats returns current counters.
func (a *Assembler) Stats() AssemblerStats { return a.stats }

// Push feeds one packet payload. pusi marks a payload_unit_start_indicator
// packet, whose first byte is the pointer field.
func (a *Assembler) Push(payload []byte, pusi bool) (sections []Section, err error) {
	if len(payload) == 0 {
		return nil, nil
	}
	if pusi {
		pointer := int(payload[0])
		if pointer > len(payload)-1 {
			a.Discard(DiscardPointer)
			return nil, fmt.Errorf("%w: pointer field %d exceeds payload %d", ErrInvalidSection, pointer, len(payload)-1)
		}
		// Pointer bytes finish the previous section, if one is active.
		if pointer > 0 && a.active {
			a.buf = append(a.buf, payload[1:1+pointer]...)
			var err error
			if sections, err = a.drain(sections); err != nil {
				return sections, err
			}
		}
		// Anything still buffered is incomplete and cannot be trusted.
		a.Discard(DiscardPointer)
		return a.pushNew(payload[1+pointer:], sections)
	}
	if !a.active {
		// Continuation without a known section start: drop it and wait for PUSI.
		return nil, nil
	}
	a.buf = append(a.buf, payload...)
	return a.drain(sections)
}

func (a *Assembler) pushNew(data []byte, sections []Section) ([]Section, error) {
	a.buf = append(a.buf, data...)
	a.active = true
	return a.drain(sections)
}

// drain extracts every complete section currently buffered.
func (a *Assembler) drain(sections []Section) ([]Section, error) {
	for {
		if len(a.buf) == 0 {
			break
		}
		if a.buf[0] == StuffingByte {
			a.buf = a.buf[:0]
			a.active = false
			break
		}
		if len(a.buf) < 3 {
			return sections, nil // need more bytes
		}
		sectionLength := int(a.buf[1]&0x0f)<<8 | int(a.buf[2])
		total := 3 + sectionLength
		if total > MaxSectionLength || sectionLength < 4 {
			a.stats.LengthErrors++
			a.active = false
			a.buf = a.buf[:0]
			return sections, fmt.Errorf("%w: section length %d outside [4,%d]", ErrInvalidSection, sectionLength, MaxSectionLength-3)
		}
		if len(a.buf) < total {
			return sections, nil // need more bytes
		}
		raw := make([]byte, total)
		copy(raw, a.buf[:total])
		a.buf = a.buf[total:]

		s, err := ParseSection(raw)
		if err != nil {
			if errors.Is(err, ErrCRCMismatch) {
				a.stats.CRCErrors++
				a.stats.Discarded++
				a.discards[DiscardCRC]++
				continue
			}
			a.stats.LengthErrors++
			a.stats.Discarded++
			a.discards[DiscardLength]++
			continue
		}
		a.stats.Completed++
		sections = append(sections, s)
	}
	if len(a.buf) == 0 {
		a.active = false
	}
	return sections, nil
}

// ErrCRCMismatch reports a section whose CRC-32 does not validate.
var ErrCRCMismatch = errors.New("psi: section CRC mismatch")

// ParseSection validates framing and CRC of one complete section.
func ParseSection(raw []byte) (Section, error) {
	if len(raw) < 3 {
		return Section{}, fmt.Errorf("%w: truncated header", ErrInvalidSection)
	}
	sectionLength := int(raw[1]&0x0f)<<8 | int(raw[2])
	if 3+sectionLength != len(raw) {
		return Section{}, fmt.Errorf("%w: header length %d != actual %d", ErrInvalidSection, sectionLength, len(raw)-3)
	}
	if len(raw) > MaxSectionLength {
		return Section{}, fmt.Errorf("%w: section size %d exceeds %d", ErrInvalidSection, len(raw), MaxSectionLength)
	}
	s := Section{
		TableID:       raw[0],
		SectionLength: uint16(sectionLength),
		Syntax:        raw[1]&0x80 != 0,
		Private:       raw[1]&0x40 != 0,
		Raw:           raw,
	}
	if s.Syntax {
		if len(raw) < 12 {
			return Section{}, fmt.Errorf("%w: syntax section too short", ErrInvalidSection)
		}
		if CRC32(raw) != 0 {
			return Section{}, fmt.Errorf("%w: table 0x%02x", ErrCRCMismatch, s.TableID)
		}
		s.Extension = uint16(raw[3])<<8 | uint16(raw[4])
		s.Version = (raw[5] >> 1) & 0x1f
		s.CurrentNext = raw[5]&0x01 != 0
		s.Number = raw[6]
		s.LastNumber = raw[7]
		s.Body = raw[8 : len(raw)-4]
	}
	return s, nil
}
