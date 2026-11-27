package ts

import (
	"tsrepair/internal/mediaerr"
)

// Table IDs.
const (
	TableIDPAT = 0x00
	TableIDPMT = 0x02
)

// ProgramEntry maps a program number to the PID carrying its PMT.
type ProgramEntry struct {
	ProgramNumber uint16
	PMTPID        uint16
}

// StreamEntry maps an elementary stream PID to its stream type.
type StreamEntry struct {
	StreamType uint8
	PID        uint16
}

// PMT is a complete program map table.
type PMT struct {
	ProgramNumber uint16
	PCRPID        uint16
	Streams       []StreamEntry
}

// Mapping is the active PAT/PMT view. It is replaced atomically only after a
// complete, CRC-valid new table set has been collected.
type Mapping struct {
	Programs []ProgramEntry
	PMTs     map[uint16]*PMT // keyed by PMT PID
}

// HasPMTPID reports whether pid carries a PMT according to the active PAT.
func (m *Mapping) HasPMTPID(pid uint16) bool {
	for _, e := range m.Programs {
		if e.PMTPID == pid {
			return true
		}
	}
	return false
}

// PSITracker collects PAT/PMT sections per (table id, version), applies the
// mapping only when every section of a new version is present and CRC-valid,
// and rejects version rollbacks while keeping the previous good mapping.
type PSITracker struct {
	ec *mediaerr.Collector

	patVersion int // -1 until the first complete PAT
	// PMT versions are tracked per PMT PID because each program numbers its
	// own PMT versions independently of the PAT and of other programs.
	pmtVersion map[uint16]int
	active     *Mapping

	// In-flight PAT versions: pending[version] -> per-section-number data.
	patPending map[uint8]map[uint8][]byte
	pmtPending map[uint8]map[uint16]map[uint8][]byte // version -> PMT PID -> section -> bytes
	assemblers map[uint16]*sectionAssembler
}

// NewPSITracker builds a tracker that records table errors into ec.
func NewPSITracker(ec *mediaerr.Collector) *PSITracker {
	return &PSITracker{
		ec:         ec,
		patVersion: -1,
		pmtVersion: map[uint16]int{},
		active:     &Mapping{PMTs: map[uint16]*PMT{}},
		patPending: map[uint8]map[uint8][]byte{},
		pmtPending: map[uint8]map[uint16]map[uint8][]byte{},
		assemblers: map[uint16]*sectionAssembler{},
	}
}

// Active returns the current mapping (never nil).
func (t *PSITracker) Active() *Mapping { return t.active }

// HandlePacket feeds a packet; PSI PIDs are assembled and parsed. Other PIDs
// are ignored so the caller can feed every packet unfiltered.
func (t *PSITracker) HandlePacket(p *Packet) {
	if p.PID != PIDPAT && !t.active.HasPMTPID(p.PID) {
		// A not-yet-known PID can never become a PMT PID without a PAT first.
		return
	}
	asm, ok := t.assemblers[p.PID]
	if !ok {
		asm = &sectionAssembler{pid: p.PID}
		t.assemblers[p.PID] = asm
	}
	for _, section := range asm.push(p, t.ec) {
		if p.PID == PIDPAT {
			t.handlePAT(section, p.Offset)
		} else {
			t.handlePMT(section, p.PID, p.Offset)
		}
	}
}

func sectionHeader(s []byte) (tableID uint8, version uint8, sectionNo, lastNo uint8) {
	tableID = s[0]
	version = (s[5] >> 1) & 0x1F
	sectionNo = s[6]
	lastNo = s[7]
	return
}

func (t *PSITracker) handlePAT(section []byte, off int64) {
	if section[0] != TableIDPAT {
		return
	}
	if !verifyCRC(section) {
		t.ec.Add(mediaerr.Recoverable(mediaerr.CodeCRCError, PIDPAT, off,
			"PAT CRC-32 mismatch", "keep previous complete mapping"))
		return
	}
	_, version, sectionNo, lastNo := sectionHeader(section)
	if t.patVersion >= 0 && int(version) < t.patVersion {
		t.ec.Add(mediaerr.Recoverable(mediaerr.CodeVersionRollback, PIDPAT, off,
			"PAT version number rolled back", "keep previous complete mapping"))
		return
	}
	if t.patPending[version] == nil {
		t.patPending[version] = map[uint8][]byte{}
	}
	t.patPending[version][sectionNo] = section

	if !t.sectionsComplete(t.patPending[version], lastNo) {
		t.ec.Add(mediaerr.Recoverable(mediaerr.CodeSectionMissing, PIDPAT, off,
			"PAT version not yet fully collected", "wait for remaining sections"))
		return
	}
	t.commitPAT(version, off)
}

func (t *PSITracker) sectionsComplete(got map[uint8][]byte, lastNo uint8) bool {
	for n := uint8(0); n <= lastNo; n++ {
		if _, ok := got[n]; !ok {
			return false
		}
	}
	return true
}

// commitPAT parses every section of a complete version and swaps the mapping
// only when all sections parse cleanly.
func (t *PSITracker) commitPAT(version uint8, off int64) {
	sections := t.patPending[version]
	var entries []ProgramEntry
	for no, s := range sections {
		lastIdx := len(s) - 4 // exclude CRC
		for i := 8; i+4 <= lastIdx; i += 4 {
			program := uint16(s[i])<<8 | uint16(s[i+1])
			pid := uint16(s[i+2]&0x1F)<<8 | uint16(s[i+3])
			if program == 0 {
				continue // network PID row
			}
			entries = append(entries, ProgramEntry{ProgramNumber: program, PMTPID: pid})
		}
		_ = no
	}
	next := &Mapping{Programs: entries, PMTs: map[uint16]*PMT{}}
	// Preserve still-valid PMTs from the previous mapping where the PMT PID
	// is referenced by the new PAT; fresh PMT versions replace them later.
	for _, e := range entries {
		if old, ok := t.active.PMTs[e.PMTPID]; ok {
			next.PMTs[e.PMTPID] = old
		}
	}
	t.active = next
	t.patVersion = int(version)
	t.patPending = map[uint8]map[uint8][]byte{}
}

func (t *PSITracker) handlePMT(section []byte, pid uint16, off int64) {
	if section[0] != TableIDPMT {
		return
	}
	if !verifyCRC(section) {
		t.ec.Add(mediaerr.Recoverable(mediaerr.CodeCRCError, pid, off,
			"PMT CRC-32 mismatch", "keep previous complete mapping"))
		return
	}
	_, version, sectionNo, lastNo := sectionHeader(section)
	if prev, ok := t.pmtVersion[pid]; ok && int(version) < prev {
		t.ec.Add(mediaerr.Recoverable(mediaerr.CodeVersionRollback, pid, off,
			"PMT version number rolled back", "keep previous complete mapping"))
		return
	}
	if t.pmtPending[version] == nil {
		t.pmtPending[version] = map[uint16]map[uint8][]byte{}
	}
	if t.pmtPending[version][pid] == nil {
		t.pmtPending[version][pid] = map[uint8][]byte{}
	}
	t.pmtPending[version][pid][sectionNo] = section

	// A PMT for a single program is normally one section; still enforce the
	// last-section-number rule generically.
	if !t.sectionsComplete(t.pmtPending[version][pid], lastNo) {
		t.ec.Add(mediaerr.Recoverable(mediaerr.CodeSectionMissing, pid, off,
			"PMT version not yet fully collected", "wait for remaining sections"))
		return
	}
	t.commitPMT(version, pid, section)
}

func (t *PSITracker) commitPMT(version uint8, pid uint16, section []byte) {
	// Layout: 0 table_id, 1-2 section_length, 3-4 program_number,
	// 5 version byte, 6-7 section numbers, 8-9 PCR_PID, 10-11 info length.
	program := uint16(section[3])<<8 | uint16(section[4])
	pcrPID := uint16(section[8]&0x1F)<<8 | uint16(section[9])
	piLen := int(section[10]&0x0F)<<8 | int(section[11])
	pos := 12 + piLen
	end := len(section) - 4 // exclude CRC
	var streams []StreamEntry
	for pos+5 <= end {
		stype := section[pos]
		esPID := uint16(section[pos+1]&0x1F)<<8 | uint16(section[pos+2])
		esInfoLen := int(section[pos+3]&0x0F)<<8 | int(section[pos+4])
		streams = append(streams, StreamEntry{StreamType: stype, PID: esPID})
		pos += 5 + esInfoLen
	}
	t.active.PMTs[pid] = &PMT{
		ProgramNumber: program,
		PCRPID:        pcrPID,
		Streams:       streams,
	}
	t.pmtVersion[pid] = int(version)
	delete(t.pmtPending, version)
}
