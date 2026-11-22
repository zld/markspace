package psi

import "errors"

// Common PSI table errors. Callers can use errors.Is to distinguish a
// rejected (corrupt/invalid) table update from a stream-level failure.
var (
	ErrBadCRC       = errors.New("psi: section CRC-32 mismatch")
	ErrInvalidTable = errors.New("psi: malformed table section")
	ErrWrongTableID = errors.New("psi: unexpected table_id")
)

// sectionHeaderVersion extracts the 5-bit table version from a section's
// common header (byte 5).
func sectionVersion(b []byte) uint8 { return (b[5] >> 1) & 0x1f }

// sectionCurrentNext returns the current_next_indicator (byte 5, low bit).
func sectionCurrentNext(b []byte) bool { return b[5]&0x01 == 1 }

// sectionNumber / lastSectionNumber are bytes 6 and 7 of the common header.
func sectionNumber(b []byte) uint8     { return b[6] }
func lastSectionNumber(b []byte) uint8 { return b[7] }

// sectionSet accumulates the sections of each table version. A version
// becomes complete once sections 0..last_section_number have all arrived
// with valid CRCs; out-of-order arrival and retransmissions are supported.
type sectionSet struct {
	sets map[uint8]map[uint8]*Section // version -> section_number -> section
}

func newSectionSet() *sectionSet {
	return &sectionSet{sets: make(map[uint8]map[uint8]*Section)}
}

// add stores one CRC-validated section. When the version is complete the
// full ordered set is returned together with its version. A bad CRC yields
// (0, nil, ErrBadCRC) and never disturbs previously stored sections.
func (s *sectionSet) add(sec *Section) (uint8, []*Section, error) {
	if len(sec.Data) < 8 || !verifyCRC(sec.Data) {
		return 0, nil, ErrBadCRC
	}
	b := sec.Data
	version, last := sectionVersion(b), lastSectionNumber(b)
	num := sectionNumber(b)
	if num > last {
		return 0, nil, ErrInvalidTable
	}
	group, ok := s.sets[version]
	if !ok {
		group = make(map[uint8]*Section)
		s.sets[version] = group
	}
	group[num] = sec // duplicate/out-of-order sections simply replace bytes

	if len(group) != int(last)+1 {
		return version, nil, nil
	}
	complete := make([]*Section, 0, int(last)+1)
	for i := uint8(0); i <= last; i++ {
		got, ok := group[i]
		if !ok {
			return version, nil, nil
		}
		if lastSectionNumber(got.Data) != last {
			return 0, nil, ErrInvalidTable
		}
		complete = append(complete, got)
	}
	return version, complete, nil
}
