package psi

import (
	"fmt"
	"sort"
)

// TableStats reports table-level activity for the JSON summary.
type TableStats struct {
	PATVersionChanges int
	PATDuplicates     int
	PMTVersionChanges int
	PMTDuplicates     int
	FutureSections    int
	PendingDiscards   int
}

type pendingTable struct {
	version  uint8
	last     uint8
	pid      uint16
	sections map[uint8]Section
}

func (p *pendingTable) complete() bool {
	if int(p.last)+1 != len(p.sections) {
		return false
	}
	for n := uint8(0); int(n) <= int(p.last); n++ {
		if _, ok := p.sections[n]; !ok {
			return false
		}
	}
	return true
}

func (p *pendingTable) ordered() []Section {
	nums := make([]int, 0, len(p.sections))
	for n := range p.sections {
		nums = append(nums, int(n))
	}
	sort.Ints(nums)
	out := make([]Section, 0, len(nums))
	for _, n := range nums {
		out = append(out, p.sections[uint8(n)])
	}
	return out
}

// Tables owns PAT/PMT version state. Mappings only become active once every
// section of one version has arrived with a valid CRC and current_next=1.
type Tables struct {
	pat        *PAT
	patPending map[uint16]*pendingTable // keyed by transport_stream_id
	patTSID    uint16

	pmts       map[uint16]*PMT // active, keyed by program number
	pmtPID     map[uint16]uint16
	pmtPending map[uint16]map[uint8]*pendingTable // program -> version -> table

	stats TableStats
}

// NewTables creates empty table state.
func NewTables() *Tables {
	return &Tables{
		patPending: make(map[uint16]*pendingTable),
		pmts:       make(map[uint16]*PMT),
		pmtPID:     make(map[uint16]uint16),
		pmtPending: make(map[uint16]map[uint8]*pendingTable),
	}
}

// Stats returns table event counters.
func (t *Tables) Stats() TableStats { return t.stats }

// PAT returns the currently effective PAT, if any.
func (t *Tables) PAT() *PAT { return t.pat }

// PATVersion reports the active PAT version.
func (t *Tables) PATVersion() (uint8, bool) {
	if t.pat == nil {
		return 0, false
	}
	return t.pat.Version, true
}

// ProgramPMTPID returns the PMT PID for program from the effective PAT.
func (t *Tables) ProgramPMTPID(program uint16) (uint16, bool) {
	if t.pat == nil {
		return 0, false
	}
	return t.pat.PMTPID(program)
}

// AddPAT consumes one CRC-valid PAT section. It returns the newly effective
// PAT when a complete version was assembled, otherwise nil.
func (t *Tables) AddPAT(s Section) (*PAT, error) {
	if s.TableID != TableIDPAT {
		return nil, fmt.Errorf("%w: table 0x%02x is not a PAT", ErrInvalidSection, s.TableID)
	}
	if !s.CurrentNext {
		t.stats.FutureSections++
		return nil, nil
	}
	key := s.Extension
	// A section repeating the version already in effect is a duplicate. This
	// also covers re-numbered sections of a completed multi-section table.
	if t.pat != nil && t.pat.TransportStreamID == key && t.pat.Version == s.Version {
		t.stats.PATDuplicates++
		return nil, nil
	}
	p := t.patPending[key]
	if p == nil || p.version != s.Version {
		t.dropPATPendingExcept(key)
		p = &pendingTable{version: s.Version, last: s.LastNumber, sections: make(map[uint8]Section)}
		t.patPending[key] = p
	}
	if _, dup := p.sections[s.Number]; dup {
		t.stats.PATDuplicates++
		return nil, nil
	}
	p.sections[s.Number] = s
	if !p.complete() {
		return nil, nil
	}
	merged, err := mergePAT(p.ordered())
	if err != nil {
		delete(t.patPending, key)
		return nil, err
	}
	delete(t.patPending, key)
	t.patTSID = key
	if t.pat != nil && t.pat.Version != merged.Version {
		t.stats.PATVersionChanges++
	}
	t.pat = merged
	return merged, nil
}

func (t *Tables) dropPATPendingExcept(keep uint16) {
	for k := range t.patPending {
		if k != keep {
			delete(t.patPending, k)
		}
	}
}

func mergePAT(ss []Section) (*PAT, error) {
	if len(ss) == 0 {
		return nil, fmt.Errorf("%w: empty PAT", ErrInvalidSection)
	}
	out := &PAT{TransportStreamID: ss[0].Extension, Version: ss[0].Version}
	seen := make(map[uint16]bool)
	for _, s := range ss {
		if s.Extension != out.TransportStreamID || s.Version != out.Version {
			return nil, fmt.Errorf("%w: inconsistent PAT sections", ErrInvalidSection)
		}
		p, err := ParsePAT(s)
		if err != nil {
			return nil, err
		}
		for _, e := range p.Programs {
			if seen[e.ProgramNumber] {
				continue
			}
			seen[e.ProgramNumber] = true
			out.Programs = append(out.Programs, e)
		}
	}
	sort.Slice(out.Programs, func(i, j int) bool {
		return out.Programs[i].ProgramNumber < out.Programs[j].ProgramNumber
	})
	return out, nil
}
