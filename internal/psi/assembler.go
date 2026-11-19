package psi

import (
	"fmt"
)

// Assembler reassembles sections for exactly one PID. It is not safe for
// concurrent use. A transport error or continuity anomaly must call Reset
// before the next payload is supplied.
type Assembler struct {
	active   bool
	buf      []byte
	expected int
}

// NewAssembler creates an empty reassembler.
func NewAssembler() *Assembler { return &Assembler{} }

// Reset drops any partially assembled section.
func (a *Assembler) Reset() {
	a.active = false
	a.buf = a.buf[:0]
	a.expected = 0
}

// Pending reports whether a section is partially buffered.
func (a *Assembler) Pending() bool { return a.active }

// Feed consumes one packet payload. pusi indicates PayloadUnitStartIndicator.
// It returns every completed section and every structural/CRC error found.
// A bad section is dropped and never returned as valid; the caller's last
// known-good table therefore cannot be overwritten by it.
func (a *Assembler) Feed(payload []byte, pusi bool) ([]*Section, []error) {
	var sections []*Section
	var errs []error

	if pusi {
		if len(payload) == 0 {
			return nil, nil
		}
		pointer := int(payload[0])
		rest := payload[1:]
		if pointer > len(rest) {
			a.Reset()
			return nil, []error{fmt.Errorf("%w: pointer_field %d > %d", ErrSectionTruncated, pointer, len(rest))}
		}
		if a.active {
			a.consume(rest[:pointer], false, &sections, &errs)
			if a.active {
				errs = append(errs, fmt.Errorf("%w: new section start before declared end", ErrSectionTruncated))
				a.Reset()
			}
		}
		a.consume(rest[pointer:], true, &sections, &errs)
		return sections, errs
	}

	if !a.active {
		// Payload bytes without a section start cannot be attributed safely.
		return nil, nil
	}
	// A section may only start in a packet with pointer_field, so trailing
	// bytes after the in-flight section ends are stuffing, never a new table.
	a.consume(payload, false, &sections, &errs)
	return sections, errs
}

// consume appends data. allowNew controls whether a section may start after
// the previous one completes; it is false for pointer_field continuation
// bytes, which may only finish the in-flight section.
func (a *Assembler) consume(data []byte, allowNew bool, sections *[]*Section, errs *[]error) {
	for len(data) > 0 {
		if !a.active {
			if !allowNew {
				return
			}
			a.active = true
			a.buf = a.buf[:0]
			a.expected = 0
		}

		if a.expected == 0 {
			n := sectionHeaderSize - len(a.buf)
			if n > len(data) {
				n = len(data)
			}
			a.buf = append(a.buf, data[:n]...)
			data = data[n:]
			if len(a.buf) < sectionHeaderSize {
				return
			}
			if a.buf[0] == 0xff {
				// 0xFF is stuffing after the final section in a packet.
				a.Reset()
				return
			}
			sectionLength := int(a.buf[1]&0x0f)<<8 | int(a.buf[2])
			total := sectionHeaderSize + sectionLength
			if sectionLength < sectionSyntaxSize+sectionCRCSize+1 || total > sectionHeaderSize+MaxSectionLength {
				*errs = append(*errs, fmt.Errorf("%w: section_length %d", ErrSectionLength, sectionLength))
				a.Reset()
				return
			}
			a.expected = total
		}

		n := a.expected - len(a.buf)
		if n > len(data) {
			n = len(data)
		}
		a.buf = append(a.buf, data[:n]...)
		data = data[n:]
		if len(a.buf) < a.expected {
			return
		}

		raw := make([]byte, len(a.buf))
		copy(raw, a.buf)
		section, err := ParseSection(raw)
		if err != nil {
			*errs = append(*errs, err)
		} else {
			*sections = append(*sections, section)
		}
		a.Reset()
		if !allowNew {
			// Without pointer_field no further section may begin in this
			// packet; anything left is stuffing.
			return
		}
	}
}

// SectionSize returns the total size for a section with the given
// section_length value (the wire field, excluding the 3 byte header).
func SectionSize(sectionLength int) int { return sectionHeaderSize + sectionLength }

// HeaderSize is the three byte section header size, exported for generators.
const HeaderSize = sectionHeaderSize

// SyntaxSize is the PSI syntax header size following the section header.
const SyntaxSize = sectionSyntaxSize

// CRCSize is the trailing CRC32 size.
const CRCSize = sectionCRCSize
