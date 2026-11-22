package psi

import (
	"fmt"
)

// TableIDPMT is the table_id of a Program Map Table section.
const TableIDPMT uint8 = 0x02

// StreamKind classifies an elementary stream by its stream_type byte.
type StreamKind string

const (
	StreamVideo   StreamKind = "video"
	StreamAudio   StreamKind = "audio"
	StreamPrivate StreamKind = "private"
	StreamOther   StreamKind = "other"
)

// ElementaryStream is one PMT entry: a PID plus its declared stream type.
type ElementaryStream struct {
	PID        uint16
	StreamType uint8
	Kind       StreamKind
}

// PMT is one committed Program Map Table version.
type PMT struct {
	ProgramNumber uint16
	Version       uint8
	PCRPID        uint16 // 0x1fff means no PCR PID is declared
	Streams       []ElementaryStream
	Sections      []*Section
}

// PMTTracker tracks PMT sections independently on each PMT PID and commits
// only fully assembled, CRC-valid versions. A failed update never replaces
// the last committed table for that PID.
type PMTTracker struct {
	state     map[uint16]*pmtState
	committed map[uint16]*PMT
	// RejectedCRC counts sections dropped for CRC mismatch across all PMT PIDs.
	RejectedCRC int
}

type pmtState struct {
	sections *sectionSet
}

// NewPMTTracker creates an empty PMT tracker.
func NewPMTTracker() *PMTTracker {
	return &PMTTracker{
		state:     make(map[uint16]*pmtState),
		committed: make(map[uint16]*PMT),
	}
}

// Push feeds one assembled section carrying a PMT. changed is true when the
// newly committed version differs from the previous commit on the same PID.
func (t *PMTTracker) Push(sec *Section) (pmt *PMT, changed bool, err error) {
	st, ok := t.state[sec.PID]
	if !ok {
		st = &pmtState{sections: newSectionSet()}
		t.state[sec.PID] = st
	}
	_, complete, err := st.sections.add(sec)
	if err != nil {
		if err == ErrBadCRC {
			t.RejectedCRC++
		}
		return nil, false, err
	}
	if complete == nil {
		return nil, false, nil
	}
	parsed, err := parsePMT(complete)
	if err != nil {
		return nil, false, err
	}
	if prev := t.committed[sec.PID]; prev != nil && parsed.Version != prev.Version {
		changed = true
	}
	t.committed[sec.PID] = parsed
	return parsed, changed, nil
}

// Current returns the last committed PMT for a PMT PID, or nil.
func (t *PMTTracker) Current(pmtPID uint16) *PMT { return t.committed[pmtPID] }

// parsePMT validates and decodes a complete, CRC-checked set of PMT sections.
func parsePMT(sections []*Section) (*PMT, error) {
	pmt := &PMT{PCRPID: 0x1fff, Sections: sections}
	for si, sec := range sections {
		b := sec.Data
		if b[0] != TableIDPMT {
			return nil, fmt.Errorf("%w: table_id 0x%02x, want 0x02", ErrWrongTableID, b[0])
		}
		if b[1]&0x80 == 0 {
			return nil, fmt.Errorf("%w: PMT section_syntax_indicator not set", ErrInvalidTable)
		}
		if !sectionCurrentNext(b) {
			return nil, fmt.Errorf("%w: PMT current_next_indicator not set", ErrInvalidTable)
		}
		program := uint16(b[3])<<8 | uint16(b[4])
		if si == 0 {
			pmt.ProgramNumber = program
			pmt.Version = sectionVersion(b)
		} else if program != pmt.ProgramNumber || sectionVersion(b) != pmt.Version {
			return nil, fmt.Errorf("%w: PMT sections disagree on program/version", ErrInvalidTable)
		}

		pmt.PCRPID = uint16(b[8]&0x1f)<<8 | uint16(b[9])
		infoLen := int(b[10]&0x0f)<<8 | int(b[11])
		sectionLen := int(b[1]&0x0f)<<8 | int(b[2])
		end := 3 + sectionLen - 4 // byte just past the last entry, before CRC
		off := 12 + infoLen
		if off > end {
			return nil, fmt.Errorf("%w: PMT program_info_length %d overruns section", ErrInvalidTable, infoLen)
		}
		for off+5 <= end {
			streamType := b[off]
			esPID := uint16(b[off+1]&0x1f)<<8 | uint16(b[off+2])
			esInfoLen := int(b[off+3]&0x0f)<<8 | int(b[off+4])
			if off+5+esInfoLen > end {
				return nil, fmt.Errorf("%w: PMT ES_info_length overruns section", ErrInvalidTable)
			}
			pmt.Streams = append(pmt.Streams, ElementaryStream{
				PID:        esPID,
				StreamType: streamType,
				Kind:       classifyStreamType(streamType),
			})
			off += 5 + esInfoLen
		}
		if off != end {
			return nil, fmt.Errorf("%w: PMT entries do not align to section end", ErrInvalidTable)
		}
	}
	return pmt, nil
}

// classifyStreamType maps the 8-bit stream_type to a coarse media category.
// Known video/audio codecs are recognized explicitly; private PES/section
// types and the 0x80-0xFF user-private range classify as private.
func classifyStreamType(t uint8) StreamKind {
	switch t {
	case 0x01, 0x02, 0x10, 0x1b, 0x20, 0x24, 0x42: // MPEG-1/2/4, H.264, HEVC, AVS
		return StreamVideo
	case 0x03, 0x04, 0x0f, 0x11, 0x81, 0x82, 0x87: // MPEG audio, AAC, AC-3, DTS, E-AC-3
		return StreamAudio
	case 0x05, 0x06:
		return StreamPrivate
	}
	if t >= 0x80 {
		return StreamPrivate
	}
	return StreamOther
}

// BuildPMTSection builds one CRC-valid PMT section (single-section PMT).
func BuildPMTSection(programNumber, pcrPID uint16, version uint8, streams []ElementaryStream) []byte {
	body := []byte{
		0xe0 | byte((pcrPID>>8)&0x1f), byte(pcrPID),
		0xf0, 0x00, // program_info_length = 0
	}
	for _, s := range streams {
		body = append(body,
			s.StreamType,
			0xe0|byte((s.PID>>8)&0x1f), byte(s.PID),
			0xf0, 0x00) // ES_info_length = 0
	}
	sectionLength := 5 + len(body) + 4
	b := sectionHeader(TableIDPMT, programNumber, version, 0, 0, sectionLength)
	b = append(b, body...)
	return appendCRC(b)
}
