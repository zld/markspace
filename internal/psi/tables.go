// PSI table assembly: parses PAT/PMT sections, tracks table versions
// and publishes a new mapping snapshot only when a complete, CRC-valid
// new version changes the effective PID mapping. The previous mapping
// stays in effect until the replacement table is fully received.
package psi

import (
	"fmt"
	"sort"

	"tsaudit/internal/model"
)

// PSI table identifiers handled by the assembler.
const (
	TableIDPAT = 0x00
	TableIDPMT = 0x02
)

// PATPID is the well-known PID of the program association table.
const PATPID = 0

// PAT is a parsed program association table.
type PAT struct {
	Version  int
	Programs map[uint16]uint16 // program number -> PMT PID
}

// PMT is a parsed program map table.
type PMT struct {
	Program int
	Version int
	Streams []model.Stream
}

// ParsePAT parses a complete, CRC-valid PAT section.
func ParsePAT(section []byte) (*PAT, error) {
	h, err := ParseHeader(section)
	if err != nil {
		return nil, err
	}
	if h.TableID != TableIDPAT {
		return nil, fmt.Errorf("table_id 0x%02x is not a PAT", h.TableID)
	}
	pat := &PAT{Version: h.Version, Programs: map[uint16]uint16{}}
	for b := section[8 : len(section)-4]; len(b) >= 4; b = b[4:] {
		program := uint16(b[0])<<8 | uint16(b[1])
		pid := uint16(b[2]&0x1F)<<8 | uint16(b[3])
		if program == 0 {
			continue // network PID, not a program
		}
		pat.Programs[program] = pid
	}
	return pat, nil
}

// ParsePMT parses a complete, CRC-valid PMT section.
func ParsePMT(section []byte) (*PMT, error) {
	h, err := ParseHeader(section)
	if err != nil {
		return nil, err
	}
	if h.TableID != TableIDPMT {
		return nil, fmt.Errorf("table_id 0x%02x is not a PMT", h.TableID)
	}
	pmt := &PMT{Program: h.TableIDExtension, Version: h.Version}
	if len(section) < 12+4 {
		return nil, fmt.Errorf("PMT truncated: %d bytes", len(section))
	}
	infoLen := int(section[10]&0x0F)<<8 | int(section[11])
	b := section[12+infoLen : len(section)-4]
	for len(b) >= 5 {
		streamType := b[0]
		pid := uint16(b[1]&0x1F)<<8 | uint16(b[2])
		esInfoLen := int(b[3]&0x0F)<<8 | int(b[4])
		pmt.Streams = append(pmt.Streams, model.Stream{
			Program:    uint16(pmt.Program),
			PID:        pid,
			StreamType: streamType,
		})
		b = b[5+esInfoLen:]
	}
	return pmt, nil
}

// parsePATSet merges all sections of a completed PAT set.
func parsePATSet(set *sectionSet) (*PAT, error) {
	merged := &PAT{Version: set.version, Programs: map[uint16]uint16{}}
	for i := 0; i <= set.last; i++ {
		pat, err := ParsePAT(set.sections[i])
		if err != nil {
			return nil, err
		}
		for program, pid := range pat.Programs {
			merged.Programs[program] = pid
		}
	}
	return merged, nil
}

// parsePMTSet merges all sections of a completed PMT set.
func parsePMTSet(set *sectionSet) (*PMT, error) {
	merged := &PMT{Version: set.version}
	for i := 0; i <= set.last; i++ {
		pmt, err := ParsePMT(set.sections[i])
		if err != nil {
			return nil, err
		}
		merged.Program = pmt.Program
		merged.Streams = append(merged.Streams, pmt.Streams...)
	}
	return merged, nil
}

// IncompleteUpdate describes a table version that started arriving but
// never completed (or failed CRC) before the input ended. The previous
// mapping remained in effect.
type IncompleteUpdate struct {
	Table          string // "PAT" or "PMT"
	PID            uint16 // PID the table arrives on
	Version        int    // version that failed to complete
	SectionsHave   int
	SectionsWant   int
	MissingPackets int // estimated packets still needed
}

// sectionSet collects the sections of one candidate table version.
type sectionSet struct {
	version  int
	last     int // last_section_number
	sections map[int][]byte
}

func newSectionSet(version int) *sectionSet {
	return &sectionSet{version: version, last: -1, sections: map[int][]byte{}}
}

func (s *sectionSet) add(h Header, raw []byte) bool {
	if s.last >= 0 && h.LastSectionNumber != s.last {
		return false // inconsistent set, ignore section
	}
	s.last = h.LastSectionNumber
	if _, dup := s.sections[h.SectionNumber]; !dup {
		s.sections[h.SectionNumber] = raw
	}
	return len(s.sections) == s.last+1
}

// Assembler maintains PAT/PMT version state and emits mapping
// snapshots. Versions are compared for inequality only, so 5-bit
// version wrap-around needs no special casing.
type Assembler struct {
	pat         *PAT
	pmtByPID    map[uint16]uint16 // PMT PID -> program number
	pmts        map[uint16]*PMT   // program number -> table
	pmtVersions map[uint16]int    // program number -> version
	patPending  *sectionSet
	pmtPending  map[uint16]*sectionSet // PMT PID -> candidate set
	patChanges  int
	pmtChanges  int
	corrupted   int
	patPublished bool
	snapshot    model.Snapshot
	hasSnapshot bool
}

// NewAssembler returns an assembler with no published tables.
func NewAssembler() *Assembler {
	return &Assembler{
		pmtByPID:    map[uint16]uint16{},
		pmts:        map[uint16]*PMT{},
		pmtVersions: map[uint16]int{},
		pmtPending:  map[uint16]*sectionSet{},
	}
}

// PMTPIDs lists the PMT PIDs of the currently published PAT.
func (a *Assembler) PMTPIDs() []uint16 {
	pids := make([]uint16, 0, len(a.pmtByPID))
	for pid := range a.pmtByPID {
		pids = append(pids, pid)
	}
	sort.Slice(pids, func(i, j int) bool { return pids[i] < pids[j] })
	return pids
}

// PATChanges is the number of published PAT version transitions.
func (a *Assembler) PATChanges() int { return a.patChanges }

// PMTChanges is the number of published PMT version transitions.
func (a *Assembler) PMTChanges() int { return a.pmtChanges }

// Corrupted is the number of sections dropped for bad CRC or headers.
func (a *Assembler) Corrupted() int { return a.corrupted }

// HasTables reports whether a valid PAT and at least one valid PMT
// have been published.
func (a *Assembler) HasTables() bool { return a.pat != nil && len(a.pmts) > 0 }

// FeedSection processes one complete section received on pid at packet
// index packetIndex. It returns a new mapping snapshot when the
// section completes a table version that changes the PID mapping.
func (a *Assembler) FeedSection(pid uint16, section []byte, packetIndex int) (model.Snapshot, bool) {
	h, err := ParseHeader(section)
	if err != nil {
		a.corrupted++
		return model.Snapshot{}, false
	}
	if !ValidCRC(section) {
		a.corrupted++
		return model.Snapshot{}, false
	}
	switch h.TableID {
	case TableIDPAT:
		if pid != PATPID {
			return model.Snapshot{}, false
		}
		if a.pat != nil && h.Version == a.pat.Version {
			return model.Snapshot{}, false // duplicate of current version
		}
		if a.patPending == nil || a.patPending.version != h.Version {
			a.patPending = newSectionSet(h.Version)
		}
		if !a.patPending.add(h, section) {
			return model.Snapshot{}, false
		}
		pat, err := parsePATSet(a.patPending)
		if err != nil {
			a.corrupted++
			return model.Snapshot{}, false
		}
		if a.patPublished {
			a.patChanges++ // count transitions only: first publication is the baseline
		}
		a.pat, a.patPending, a.patPublished = pat, nil, true
		a.pmtByPID = map[uint16]uint16{}
		for program, pmtPID := range pat.Programs {
			a.pmtByPID[pmtPID] = program
		}
		for program := range a.pmts {
			if _, ok := pat.Programs[program]; !ok {
				delete(a.pmts, program)
				delete(a.pmtVersions, program)
			}
		}
	case TableIDPMT:
		program, ok := a.pmtByPID[pid]
		if !ok {
			return model.Snapshot{}, false // PMT on a PID the PAT does not reference
		}
		if cur, ok := a.pmts[program]; ok && h.Version == cur.Version {
			return model.Snapshot{}, false // duplicate of current version
		}
		pending := a.pmtPending[pid]
		if pending == nil || pending.version != h.Version {
			pending = newSectionSet(h.Version)
			a.pmtPending[pid] = pending
		}
		if !pending.add(h, section) {
			return model.Snapshot{}, false
		}
		pmt, err := parsePMTSet(pending)
		if err != nil {
			a.corrupted++
			return model.Snapshot{}, false
		}
		pmt.Program = int(program) // PAT is authoritative for the mapping
		if _, ok := a.pmts[program]; ok {
			a.pmtChanges++ // count transitions only: first publication is the baseline
		}
		a.pmts[program], a.pmtVersions[program] = pmt, pmt.Version
		delete(a.pmtPending, pid)
	default:
		return model.Snapshot{}, false
	}
	return a.maybeSnapshot(packetIndex)
}

// maybeSnapshot publishes a snapshot when the stream mapping changed.
// Version-only updates bump the change counters but do not open a new
// mapping interval; the next published snapshot carries the versions
// current at that boundary.
func (a *Assembler) maybeSnapshot(packetIndex int) (model.Snapshot, bool) {
	var streams []model.Stream
	for _, pmt := range a.pmts {
		streams = append(streams, pmt.Streams...)
	}
	if len(streams) == 0 {
		return model.Snapshot{}, false
	}
	index := 0
	if a.hasSnapshot {
		index = a.snapshot.Index + 1
	}
	snap := model.NewSnapshot(index, packetIndex, a.pat.Version, a.pmtVersions, streams)
	if a.hasSnapshot && model.SameMapping(a.snapshot.Streams, snap.Streams) {
		return model.Snapshot{}, false
	}
	a.snapshot, a.hasSnapshot = snap, true
	return snap, true
}

// Incomplete reports table versions still pending at end of input.
// pendingBytes carries per-PID bytes still missing from a partially
// reassembled section (from the reader stage), converted to packets.
func (a *Assembler) Incomplete(pendingBytes map[uint16]int) []IncompleteUpdate {
	var out []IncompleteUpdate
	missing := func(pid uint16, s *sectionSet) int {
		packets := (pendingBytes[pid] + 183) / 184
		if want := s.last + 1 - len(s.sections); want > packets {
			packets = want
		}
		return packets
	}
	if a.patPending != nil {
		out = append(out, IncompleteUpdate{
			Table: "PAT", PID: PATPID, Version: a.patPending.version,
			SectionsHave: len(a.patPending.sections), SectionsWant: a.patPending.last + 1,
			MissingPackets: missing(PATPID, a.patPending),
		})
	}
	for pid, s := range a.pmtPending {
		out = append(out, IncompleteUpdate{
			Table: "PMT", PID: pid, Version: s.version,
			SectionsHave: len(s.sections), SectionsWant: s.last + 1,
			MissingPackets: missing(pid, s),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}
