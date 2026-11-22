package psi

import "errors"

// Table IDs handled by this parser.
const (
	TableIDPAT = 0x00
	TableIDPMT = 0x02
)

// CompleteTable is a fully assembled, CRC-validated table version.
type CompleteTable struct {
	PID        uint16 // PID the table was carried on
	TableID    uint8
	Extension  uint16 // transport_stream_id (PAT) or program_number (PMT)
	Version    uint8
	Bodies     [][]byte // section bodies ordered by section number
	NewVersion bool     // false when this exact version was already published
}

type tableKey struct {
	pid uint16
	id  uint8
	ext uint16
}

type versionBuf struct {
	last uint8
	secs map[uint8][]byte
}

type tableState struct {
	pending   map[uint8]*versionBuf // by version number
	published int                    // last published version, -1 = none
}

// Tracker assembles sections into versioned tables. A table is published
// only when every section of one version is present and CRC-valid; an
// incomplete new version never displaces the published one.
type Tracker struct {
	tables map[tableKey]*tableState
}

// NewTracker builds an empty tracker.
func NewTracker() *Tracker {
	return &Tracker{tables: map[tableKey]*tableState{}}
}

// Add ingests one section. It returns a non-nil table when the section
// completes a version; duplicate sections and duplicate versions are
// absorbed without effect.
func (t *Tracker) Add(pid uint16, s Section) *CompleteTable {
	if !s.CurrentNext {
		return nil // only current tables take effect
	}
	k := tableKey{pid, s.TableID, s.TableIDExtension}
	ts := t.tables[k]
	if ts == nil {
		ts = &tableState{pending: map[uint8]*versionBuf{}, published: -1}
		t.tables[k] = ts
	}
	vb := ts.pending[s.Version]
	if vb == nil {
		vb = &versionBuf{last: s.LastSectionNumber, secs: map[uint8][]byte{}}
		ts.pending[s.Version] = vb
	}
	if vb.last != s.LastSectionNumber || s.SectionNumber > vb.last {
		return nil // inconsistent section, ignore
	}
	if _, dup := vb.secs[s.SectionNumber]; dup {
		return nil // duplicate section
	}
	vb.secs[s.SectionNumber] = s.Body
	if len(vb.secs) != int(vb.last)+1 {
		return nil // version still incomplete
	}
	bodies := make([][]byte, 0, len(vb.secs))
	for i := 0; i <= int(vb.last); i++ {
		bodies = append(bodies, vb.secs[uint8(i)])
	}
	delete(ts.pending, s.Version)
	ct := &CompleteTable{
		PID:       pid,
		TableID:   s.TableID,
		Extension: s.TableIDExtension,
		Version:   s.Version,
		Bodies:    bodies,
	}
	if ts.published != int(s.Version) {
		// Any version different from the published one counts as new;
		// this naturally handles 5-bit version wrap-around.
		ct.NewVersion = true
		ts.published = int(s.Version)
	}
	return ct
}

// PATEntry maps one program number to its PMT PID.
type PATEntry struct {
	Program uint16
	PMTPID  uint16
}

// ParsePAT decodes program entries from a complete PAT.
func ParsePAT(t *CompleteTable) ([]PATEntry, error) {
	var out []PATEntry
	for _, body := range t.Bodies {
		for len(body) >= 4 {
			program := uint16(body[0])<<8 | uint16(body[1])
			pid := uint16(body[2]&0x1F)<<8 | uint16(body[3])
			if program != 0 { // program 0 carries the network PID, skip
				out = append(out, PATEntry{Program: program, PMTPID: pid})
			}
			body = body[4:]
		}
	}
	return out, nil
}

// PMTStream describes one elementary stream of a PMT.
type PMTStream struct {
	StreamType uint8
	PID        uint16
}

// ParsePMT decodes the program number and elementary stream list of a
// complete PMT. Private descriptor semantics are not interpreted.
func ParsePMT(t *CompleteTable) (program uint16, streams []PMTStream, err error) {
	program = t.Extension
	for _, body := range t.Bodies {
		if len(body) < 4 {
			return 0, nil, errors.New("psi: short PMT body")
		}
		progInfoLen := int(body[2]&0x0F)<<8 | int(body[3])
		body = body[4:]
		if progInfoLen > len(body) {
			return 0, nil, errors.New("psi: program_info_length overruns section")
		}
		body = body[progInfoLen:]
		for len(body) >= 5 {
			st := PMTStream{
				StreamType: body[0],
				PID:        uint16(body[1]&0x1F)<<8 | uint16(body[2]),
			}
			esInfoLen := int(body[3]&0x0F)<<8 | int(body[4])
			body = body[5:]
			if esInfoLen > len(body) {
				return 0, nil, errors.New("psi: es_info_length overruns section")
			}
			body = body[esInfoLen:]
			streams = append(streams, st)
		}
	}
	return program, streams, nil
}
