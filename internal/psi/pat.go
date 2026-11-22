package psi

import (
	"fmt"

	"tsrebuild/internal/tsreader"
)

// TableIDPAT is the table_id of a Program Association Table.
const TableIDPAT uint8 = 0x00

// PATProgram maps one program_number to the PID carrying its PMT.
type PATProgram struct {
	ProgramNumber uint16
	PMTPID        uint16
}

// PAT is one committed Program Association Table version.
type PAT struct {
	TransportStreamID uint16
	Version           uint8
	NetworkPID        uint16 // 0x1fff when the stream carries no NIT
	Programs          []PATProgram
	Sections          []*Section
}

// PATTracker assembles PAT sections per version and only commits versions
// whose sections are all present and CRC-valid. A corrupt or partial update
// never replaces the last committed table.
type PATTracker struct {
	sections  *sectionSet
	committed *PAT
	// RejectedCRC counts sections dropped for CRC mismatch.
	RejectedCRC int
}

// NewPATTracker creates an empty PAT tracker.
func NewPATTracker() *PATTracker {
	return &PATTracker{sections: newSectionSet()}
}

// Push feeds one assembled section from PID 0. It returns the table when a
// full version becomes available; changed is true only when its version
// differs from the previously committed one (the first commit is not a change).
func (t *PATTracker) Push(sec *Section) (pat *PAT, changed bool, err error) {
	if sec.PID != tsreader.PIDPAT {
		return nil, false, fmt.Errorf("%w: section on PID %d, expected PAT PID 0", ErrInvalidTable, sec.PID)
	}
	_, complete, err := t.sections.add(sec)
	if err != nil {
		if err == ErrBadCRC {
			t.RejectedCRC++
		}
		return nil, false, err
	}
	if complete == nil {
		return nil, false, nil // wait for the remaining sections of this version
	}
	parsed, err := parsePAT(complete)
	if err != nil {
		return nil, false, err
	}
	if t.committed != nil && parsed.Version != t.committed.Version {
		changed = true
	}
	t.committed = parsed
	return parsed, changed, nil
}

// Current returns the last successfully committed PAT, or nil.
func (t *PATTracker) Current() *PAT { return t.committed }

// parsePAT validates and decodes a complete, CRC-checked set of PAT sections.
func parsePAT(sections []*Section) (*PAT, error) {
	pat := &PAT{NetworkPID: 0x1fff, Sections: sections}
	for si, sec := range sections {
		b := sec.Data
		if b[0] != TableIDPAT {
			return nil, fmt.Errorf("%w: table_id 0x%02x, want 0x00", ErrWrongTableID, b[0])
		}
		if b[1]&0x80 == 0 {
			return nil, fmt.Errorf("%w: PAT section_syntax_indicator not set", ErrInvalidTable)
		}
		if !sectionCurrentNext(b) {
			return nil, fmt.Errorf("%w: PAT current_next_indicator not set", ErrInvalidTable)
		}
		tsid := uint16(b[3])<<8 | uint16(b[4])
		if si == 0 {
			pat.TransportStreamID = tsid
			pat.Version = sectionVersion(b)
		} else if tsid != pat.TransportStreamID || sectionVersion(b) != pat.Version {
			return nil, fmt.Errorf("%w: PAT sections disagree on ts_id/version", ErrInvalidTable)
		}

		sectionLen := int(b[1]&0x0f)<<8 | int(b[2])
		entryBytes := sectionLen - 9 // 5 header bytes after length field + 4 CRC bytes
		if entryBytes < 0 || entryBytes%4 != 0 {
			return nil, fmt.Errorf("%w: PAT section %d has %d non-divisible entry bytes", ErrInvalidTable, si, entryBytes)
		}
		for off := 8; off < 8+entryBytes; off += 4 {
			program := uint16(b[off])<<8 | uint16(b[off+1])
			pid := uint16(b[off+2]&0x1f)<<8 | uint16(b[off+3])
			if program == 0 {
				pat.NetworkPID = pid
				continue
			}
			pat.Programs = append(pat.Programs, PATProgram{ProgramNumber: program, PMTPID: pid})
		}
	}
	return pat, nil
}
