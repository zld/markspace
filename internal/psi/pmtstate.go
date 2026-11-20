package psi

import (
	"fmt"
	"sort"
)

// AddPMT consumes one CRC-valid PMT section received on pid. It returns the
// newly effective PMT when a complete version was assembled, otherwise nil.
func (t *Tables) AddPMT(pid uint16, s Section) (*PMT, error) {
	if s.TableID != TableIDPMT {
		return nil, fmt.Errorf("%w: table 0x%02x is not a PMT", ErrInvalidSection, s.TableID)
	}
	if !s.CurrentNext {
		t.stats.FutureSections++
		return nil, nil
	}
	program := s.Extension
	// Same-version repeats of the effective PMT on its active PID are duplicates.
	if old := t.pmts[program]; old != nil && old.Version == s.Version && t.pmtPID[program] == pid {
		t.stats.PMTDuplicates++
		return nil, nil
	}
	byVer := t.pmtPending[program]
	if byVer == nil {
		byVer = make(map[uint8]*pendingTable)
		t.pmtPending[program] = byVer
	}
	p := byVer[s.Version]
	if p == nil {
		for v := range byVer {
			if v != s.Version {
				delete(byVer, v)
			}
		}
		p = &pendingTable{version: s.Version, last: s.LastNumber, pid: pid, sections: make(map[uint8]Section)}
		byVer[s.Version] = p
	}
	if _, dup := p.sections[s.Number]; dup {
		t.stats.PMTDuplicates++
		return nil, nil
	}
	p.sections[s.Number] = s
	if !p.complete() {
		return nil, nil
	}
	merged, err := mergePMT(pid, p.ordered())
	if err != nil {
		delete(byVer, s.Version)
		return nil, err
	}
	delete(byVer, s.Version)
	if len(byVer) == 0 {
		delete(t.pmtPending, program)
	}
	if old := t.pmts[program]; old != nil && old.Version != merged.Version {
		t.stats.PMTVersionChanges++
	}
	t.pmts[program] = merged
	t.pmtPID[program] = pid
	return merged, nil
}

func mergePMT(pid uint16, ss []Section) (*PMT, error) {
	if len(ss) == 0 {
		return nil, fmt.Errorf("%w: empty PMT", ErrInvalidSection)
	}
	out := &PMT{ProgramNumber: ss[0].Extension, Version: ss[0].Version}
	seen := make(map[uint16]bool)
	for i, s := range ss {
		if s.Extension != out.ProgramNumber || s.Version != out.Version {
			return nil, fmt.Errorf("%w: inconsistent PMT sections", ErrInvalidSection)
		}
		p, err := ParsePMT(s)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			out.PCRPID = p.PCRPID
		}
		for _, es := range p.Streams {
			if seen[es.PID] {
				continue
			}
			seen[es.PID] = true
			out.Streams = append(out.Streams, es)
		}
	}
	sort.Slice(out.Streams, func(i, j int) bool { return out.Streams[i].PID < out.Streams[j].PID })
	return out, nil
}

// PMT returns the active PMT for program, but only while the active PAT still
// maps that program to the PID the PMT arrived on.
func (t *Tables) PMT(program uint16) (*PMT, bool) {
	p := t.pmts[program]
	if p == nil || t.pat == nil {
		return nil, false
	}
	pid, ok := t.pat.PMTPID(program)
	if !ok || pid != t.pmtPID[program] {
		return nil, false
	}
	return p, true
}

// PMTPIDs returns the sorted set of PMT PIDs referenced by the active PAT.
func (t *Tables) PMTPIDs() []uint16 {
	if t.pat == nil {
		return nil
	}
	seen := make(map[uint16]bool)
	var out []uint16
	for _, e := range t.pat.Programs {
		if !seen[e.PMTPID] {
			seen[e.PMTPID] = true
			out = append(out, e.PMTPID)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Programs returns the sorted program numbers in the active PAT.
func (t *Tables) Programs() []uint16 {
	if t.pat == nil {
		return nil
	}
	out := make([]uint16, 0, len(t.pat.Programs))
	for _, e := range t.pat.Programs {
		out = append(out, e.ProgramNumber)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// DiscardPendingPID abandons incomplete tables fed by pid (0 means PAT). It is
// used after packet loss so a stale partial table cannot complete later.
func (t *Tables) DiscardPendingPID(pid uint16) {
	if pid == 0 {
		if len(t.patPending) > 0 {
			t.stats.PendingDiscards += len(t.patPending)
			t.patPending = make(map[uint16]*pendingTable)
		}
		return
	}
	for program, byVer := range t.pmtPending {
		for v, p := range byVer {
			if p.pid == pid {
				delete(byVer, v)
				t.stats.PendingDiscards++
			}
		}
		if len(byVer) == 0 {
			delete(t.pmtPending, program)
		}
	}
}
