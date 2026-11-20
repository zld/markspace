package psi

import "fmt"

// Table IDs handled by the tracker.
const (
	TableIDPAT = 0x00
	TableIDPMT = 0x02
)

// Stream is one elementary stream entry of a PMT.
type Stream struct {
	PID        uint16
	StreamType byte // kept as a raw number; unknown types are preserved
}

// ProgramSnapshot is the committed view of one program.
type ProgramSnapshot struct {
	Number     uint16
	PMTPID     uint16
	PMTVersion byte
	HasPMT     bool
	PCRPID     uint16
	Streams    []Stream
	RawPMT     []byte // raw PMT section that produced this entry
}

// Snapshot is an immutable, committed program table state. Commits produce
// new snapshots; older ones stay valid for reuse (e.g. by the slicer).
type Snapshot struct {
	TSID        uint16
	PATVersion  byte
	HasPAT      bool
	Programs    map[uint16]*ProgramSnapshot
	RawPAT      []byte
	CommittedAt int64 // file offset of the completing section
}

// Complete reports whether a PAT and every referenced PMT are present.
func (s *Snapshot) Complete() bool {
	if s == nil || !s.HasPAT {
		return false
	}
	for _, p := range s.Programs {
		if !p.HasPMT {
			return false
		}
	}
	return true
}

func (s *Snapshot) clone() *Snapshot {
	n := &Snapshot{
		TSID:        s.TSID,
		PATVersion:  s.PATVersion,
		HasPAT:      s.HasPAT,
		Programs:    make(map[uint16]*ProgramSnapshot, len(s.Programs)),
		RawPAT:      s.RawPAT,
		CommittedAt: s.CommittedAt,
	}
	for k, p := range s.Programs {
		cp := *p
		cp.Streams = append([]Stream(nil), p.Streams...)
		n.Programs[k] = &cp
	}
	return n
}

type tableKey struct {
	tableID byte
	ext     uint16
	version byte
}

type pendingTable struct {
	sections [][]byte
	got      int
}

// VersionStats counts committed version transitions. The first committed
// version of a table never counts; re-broadcasts of the committed version
// do not count either.
type VersionStats struct {
	PATChanges int            // keyed by TSID: this tool tracks a single TS
	PMTChanges map[uint16]int // program number -> committed version changes
}

// TableTracker collects sections into pending tables and commits complete,
// CRC-valid, current_next=1 tables into snapshots.
type TableTracker struct {
	pending   map[tableKey]*pendingTable
	patVer    map[uint16]byte  // TSID -> last committed PAT version
	patSeen   map[uint16]bool  // TSID -> any PAT committed yet
	pmtVer    map[uint16]byte  // program -> last committed PMT version
	pmtSeen   map[uint16]bool  // program -> any PMT committed yet
	Stats     VersionStats
	current   *Snapshot
}

// NewTableTracker returns an empty tracker.
func NewTableTracker() *TableTracker {
	return &TableTracker{
		pending: make(map[tableKey]*pendingTable),
		patVer:  make(map[uint16]byte),
		patSeen: make(map[uint16]bool),
		pmtVer:  make(map[uint16]byte),
		pmtSeen: make(map[uint16]bool),
		Stats:   VersionStats{PMTChanges: make(map[uint16]int)},
		current: &Snapshot{Programs: make(map[uint16]*ProgramSnapshot)},
	}
}

// Current returns the latest committed snapshot (never nil).
func (t *TableTracker) Current() *Snapshot { return t.current }

// Feed folds one assembled section into the tracker. Sections with
// current_next_indicator=0 are syntactically validated but never committed.
func (t *TableTracker) Feed(s Section) error {
	switch s.TableID {
	case TableIDPAT, TableIDPMT:
	default:
		return nil // other tables are not tracked
	}
	if !s.CurrentNext() {
		return nil
	}
	key := tableKey{tableID: s.TableID, ext: s.TableIDExtension(), version: s.Version()}
	num, last := s.SectionNumber(), s.LastSectionNumber()
	if num > last || last > 31 {
		return fmt.Errorf("psi: implausible section numbers %d/%d", num, last)
	}
	pt := t.pending[key]
	if pt == nil || len(pt.sections) != int(last)+1 {
		pt = &pendingTable{sections: make([][]byte, int(last)+1)}
		t.pending[key] = pt
	}
	if pt.sections[num] == nil {
		pt.sections[num] = s.Data
		pt.got++
	}
	if pt.got < len(pt.sections) {
		return nil // table still incomplete; previous snapshot stays in force
	}
	full := concat(pt.sections)
	delete(t.pending, key)
	if s.TableID == TableIDPAT {
		return t.commitPAT(key.ext, key.version, full, s.Offset)
	}
	return t.commitPMT(key.ext, key.version, full, s.Offset)
}

func concat(parts [][]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// commitPAT applies a complete PAT: TSID -> programs with PMT PIDs.
func (t *TableTracker) commitPAT(tsid uint16, version byte, sec []byte, off int64) error {
	body := sectionBody(sec)
	if len(body)%4 != 0 {
		return fmt.Errorf("psi: PAT body length %d not a multiple of 4", len(body))
	}
	if t.patSeen[tsid] && t.patVer[tsid] != version {
		t.Stats.PATChanges++
	}
	t.patSeen[tsid] = true
	t.patVer[tsid] = version

	next := t.current.clone()
	next.TSID = tsid
	next.PATVersion = version
	next.HasPAT = true
	next.RawPAT = append([]byte(nil), sec...)
	next.CommittedAt = off
	// Programs dropped from the new PAT version disappear from the map.
	for num := range next.Programs {
		delete(next.Programs, num)
	}
	for i := 0; i+4 <= len(body); i += 4 {
		num := uint16(body[i])<<8 | uint16(body[i+1])
		pid := uint16(body[i+2]&0x1f)<<8 | uint16(body[i+3])
		if num == 0 {
			continue // network PID (NIT), not a program
		}
		prev := t.current.Programs[num]
		ps := &ProgramSnapshot{Number: num, PMTPID: pid}
		if prev != nil && prev.PMTPID == pid {
			*ps = *prev // carry PMT state across PAT re-commits
		}
		next.Programs[num] = ps
	}
	t.current = next
	return nil
}

// commitPMT applies a complete PMT for one program.
func (t *TableTracker) commitPMT(program uint16, version byte, sec []byte, off int64) error {
	if len(sec) < 12 {
		return fmt.Errorf("psi: PMT too short (%d bytes)", len(sec))
	}
	pcrPID := uint16(sec[8]&0x1f)<<8 | uint16(sec[9])
	pil := int(sec[10]&0x0f)<<8 | int(sec[11])
	body := sectionBody(sec)
	if 4+pil > len(body) {
		return fmt.Errorf("psi: program_info_length overruns PMT")
	}
	rest := body[4+pil:]
	var streams []Stream
	for len(rest) >= 5 {
		st := rest[0]
		pid := uint16(rest[1]&0x1f)<<8 | uint16(rest[2])
		eil := int(rest[3]&0x0f)<<8 | int(rest[4])
		if 5+eil > len(rest) {
			return fmt.Errorf("psi: ES_info_length overruns PMT")
		}
		streams = append(streams, Stream{PID: pid, StreamType: st})
		rest = rest[5+eil:]
	}
	if t.pmtSeen[program] && t.pmtVer[program] != version {
		t.Stats.PMTChanges[program]++
	}
	t.pmtSeen[program] = true
	t.pmtVer[program] = version

	next := t.current.clone()
	ps := next.Programs[program]
	if ps == nil {
		ps = &ProgramSnapshot{Number: program}
		next.Programs[program] = ps
	}
	ps.PMTVersion = version
	ps.HasPMT = true
	ps.PCRPID = pcrPID
	ps.Streams = streams
	ps.RawPMT = append([]byte(nil), sec...)
	next.CommittedAt = off
	t.current = next
	return nil
}

// sectionBody strips the 8-byte header (through last_section_number) and
// the 4-byte CRC, leaving the table-specific body.
func sectionBody(sec []byte) []byte {
	sl := int(sec[1]&0x0f)<<8 | int(sec[2])
	end := 3 + sl - 4
	if end < 8 || end > len(sec) {
		return nil
	}
	return sec[8:end]
}
