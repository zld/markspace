package program

import (
	"fmt"
	"sort"

	"tsinspect/internal/psi"
)

// ApplyPMT feeds one CRC-valid PMT section received on pmtPID. Sections for a
// program whose PAT entry is not yet known are retained in a small bounded
// table so a mid-stream start can still resolve tables that arrive before the
// PAT; they are never treated as effective until the PAT confirms the PID.
func (m *Manager) ApplyPMT(s *psi.Section, pmtPID uint16) error {
	if s.TableID != psi.TableIDPMT {
		return fmt.Errorf("program: table 0x%02x on PID %d is not PMT", s.TableID, pmtPID)
	}
	pmt, err := psi.ParsePMT(s)
	if err != nil {
		return err
	}
	if !pmt.CurrentNext {
		m.noteOnce(fmt.Sprintf("pmt-next-%d-%d", pmtPID, pmt.Version), Event{
			Kind: "pmt", ProgramNumber: pmt.ProgramNumber, PID: pmtPID, Version: pmt.Version,
			Status: StatusIgnoredNextTable, Detail: "current_next_indicator=0",
		})
		return nil
	}

	program, bound := m.programForPID(pmtPID)
	if !bound {
		first := m.rememberUnbound(pmtPID, pmt)
		if first {
			m.orphanCount++
		}
		m.noteOnce(fmt.Sprintf("orphan-%d-%d", pmtPID, pmt.Version), Event{
			Kind: "pmt", ProgramNumber: pmt.ProgramNumber, PID: pmtPID, Version: pmt.Version,
			Status: StatusOrphanPMT, Detail: "no effective PAT entry points at this PMT PID",
		})
		return nil
	}
	if program != pmt.ProgramNumber {
		m.event(Event{Kind: "pmt", ProgramNumber: program, PID: pmtPID, Version: pmt.Version,
			Status: StatusPMTProgramMismatch,
			Detail: fmt.Sprintf("PMT declares program_number %d, PAT binds PID %d to program %d", pmt.ProgramNumber, pmtPID, program)})
		return fmt.Errorf("psi: PMT on PID %d declares program %d, PAT says %d", pmtPID, pmt.ProgramNumber, program)
	}

	if eff := m.pmts[program]; eff != nil && versionRelation(eff.Version, pmt.Version) < 0 {
		m.noteOnce(fmt.Sprintf("pmt-stale-%d-%d", pmtPID, pmt.Version), Event{
			Kind: "pmt", ProgramNumber: program, PID: pmtPID, Version: pmt.Version,
			Status: StatusStaleVersion, Detail: fmt.Sprintf("older than effective version %d", eff.Version),
		})
		return nil
	}

	cand := m.pmtPending[program]
	if cand == nil || cand.version != pmt.Version || cand.last != pmt.LastSectionNumber {
		if cand != nil && !cand.complete {
			m.event(Event{Kind: "pmt", ProgramNumber: program, PID: pmtPID, Version: cand.version,
				Status: StatusPending, Detail: "version superseded before all sections arrived"})
		}
		cand = &pmtCandidate{version: pmt.Version, last: pmt.LastSectionNumber, sections: map[uint8]*psi.PMT{}}
		m.pmtPending[program] = cand
	}
	if existing, ok := cand.sections[pmt.SectionNumber]; ok && !sameSectionContent(existing, pmt) {
		m.event(Event{Kind: "pmt", ProgramNumber: program, PID: pmtPID, Version: pmt.Version,
			Status: StatusSameVersionConflict,
			Detail: fmt.Sprintf("section %d changed content between retransmissions", pmt.SectionNumber)})
		return &ErrConflict{Kind: "PMT", Program: program, Version: pmt.Version,
			Detail: fmt.Sprintf("section %d changed content between retransmissions", pmt.SectionNumber)}
	}
	cand.sections[pmt.SectionNumber] = pmt
	if len(cand.sections) != int(cand.last)+1 {
		return nil
	}
	state, err := mergePMTSections(program, pmtPID, cand)
	if err != nil {
		m.event(Event{Kind: "pmt", ProgramNumber: program, PID: pmtPID, Version: pmt.Version,
			Status: StatusInconsistentSections, Detail: err.Error()})
		delete(m.pmtPending, program)
		return err
	}
	cand.merged, cand.complete = state, true
	return m.commitPMT(state)
}

func (m *Manager) programForPID(pmtPID uint16) (uint16, bool) {
	return m.ProgramForPMTPID(pmtPID)
}

// rememberUnbound stores the newest orphan PMT section for a PID, returning
// true when this PID had no cached PMT yet.
func (m *Manager) rememberUnbound(pmtPID uint16, pmt *psi.PMT) (first bool) {
	if _, ok := m.unbound[pmtPID]; !ok {
		if len(m.unbound) >= MaxUnboundPMTs {
			oldest := m.unboundSeq[0]
			m.unboundSeq = m.unboundSeq[1:]
			delete(m.unbound, oldest)
		}
		m.unboundSeq = append(m.unboundSeq, pmtPID)
		first = true
	}
	m.unbound[pmtPID] = &unboundPMT{program: pmt.ProgramNumber, version: pmt.Version, pmt: pmt}
	return first
}

// AdoptUnboundPMTs re-applies PMT sections that arrived before the PAT became
// effective and whose PID the new PAT now binds. Called after a PAT commit.
func (m *Manager) AdoptUnboundPMTs() {
	if len(m.unbound) == 0 {
		return
	}
	pids := make([]uint16, 0, len(m.unbound))
	for pid := range m.unbound {
		pids = append(pids, pid)
	}
	sort.Slice(pids, func(i, j int) bool { return pids[i] < pids[j] })
	for _, pid := range pids {
		u := m.unbound[pid]
		program, ok := m.ProgramForPMTPID(pid)
		if !ok {
			continue
		}
		delete(m.unbound, pid)
		if u.program != program {
			continue
		}
		m.applyParsedPMT(pid, program, u.pmt)
	}
	kept := m.unboundSeq[:0]
	for _, pid := range m.unboundSeq {
		if _, ok := m.unbound[pid]; ok {
			kept = append(kept, pid)
		}
	}
	m.unboundSeq = kept
}

// applyParsedPMT feeds an already parsed PMT through candidate assembly.
func (m *Manager) applyParsedPMT(pid, program uint16, pmt *psi.PMT) {
	cand := &pmtCandidate{version: pmt.Version, last: pmt.LastSectionNumber, sections: map[uint8]*psi.PMT{}}
	cand.sections[pmt.SectionNumber] = pmt
	if len(cand.sections) != int(cand.last)+1 {
		m.pmtPending[program] = cand
		return
	}
	state, err := mergePMTSections(program, pid, cand)
	if err != nil {
		return
	}
	cand.merged, cand.complete = state, true
	m.commitPMT(state)
}

func mergePMTSections(program, pmtPID uint16, c *pmtCandidate) (*PMTState, error) {
	state := &PMTState{ProgramNumber: program, PID: pmtPID, Version: c.version}
	var pcrPID uint16
	havePCR := false
	seen := map[uint16]uint8{}
	for i := 0; i <= int(c.last); i++ {
		part, ok := c.sections[uint8(i)]
		if !ok {
			return nil, fmt.Errorf("missing section %d of %d", i, c.last)
		}
		if part.ProgramNumber != program {
			return nil, fmt.Errorf("section %d declares program %d, want %d", i, part.ProgramNumber, program)
		}
		if i == 0 || !havePCR {
			pcrPID, havePCR = part.PCRPID, true
		} else if part.PCRPID != pcrPID {
			return nil, fmt.Errorf("PCR PID differs between sections (%d vs %d)", pcrPID, part.PCRPID)
		}
		for _, es := range part.Streams {
			if st, ok := seen[es.PID]; ok {
				if st != es.StreamType {
					return nil, fmt.Errorf("PID %d declared as stream_type 0x%02x and 0x%02x", es.PID, st, es.StreamType)
				}
				return nil, fmt.Errorf("PID %d declared in more than one section", es.PID)
			}
			seen[es.PID] = es.StreamType
			state.Streams = append(state.Streams, Stream{PID: es.PID, StreamType: es.StreamType})
		}
	}
	if havePCR {
		state.PCRPID = pcrPID
	}
	sort.Slice(state.Streams, func(i, j int) bool { return state.Streams[i].PID < state.Streams[j].PID })
	return state, nil
}

func sameSectionContent(a, b *psi.PMT) bool {
	if a.PCRPID != b.PCRPID || len(a.Streams) != len(b.Streams) || a.Version != b.Version {
		return false
	}
	for i := range a.Streams {
		if a.Streams[i].PID != b.Streams[i].PID || a.Streams[i].StreamType != b.Streams[i].StreamType {
			return false
		}
	}
	return true
}

func (m *Manager) commitPMT(next *PMTState) error {
	prev := m.pmts[next.ProgramNumber]
	if prev != nil && prev.Version == next.Version {
		if prev.PCRPID != next.PCRPID || !sameStreams(prev.Streams, next.Streams) {
			detail := describePMTDiff(prev, next)
			m.event(Event{Kind: "pmt", ProgramNumber: next.ProgramNumber, PID: next.PID, Version: next.Version,
				Status: StatusSameVersionConflict, Detail: detail})
			return &ErrConflict{Kind: "PMT", Program: next.ProgramNumber, Version: next.Version, Detail: detail}
		}
		delete(m.pmtPending, next.ProgramNumber)
		return nil
	}

	status, detail := StatusApplied, "initial PMT"
	if prev != nil {
		detail = describePMTDiff(prev, next)
		if wrappedVersion(prev.Version, next.Version) {
			status = StatusAppliedVersionWrap
			detail += "; 5-bit version wrapped"
		}
		m.pmtChanges++
	}
	m.event(Event{Kind: "pmt", ProgramNumber: next.ProgramNumber, PID: next.PID, Version: next.Version,
		Status: status, Detail: detail, EffectiveVersion: effective(next.Version)})
	m.pmts[next.ProgramNumber] = next
	delete(m.pmtPending, next.ProgramNumber)
	return nil
}

func describePMTDiff(a, b *PMTState) string {
	out := fmt.Sprintf("pcr_pid %d->%d streams %d->%d", a.PCRPID, b.PCRPID, len(a.Streams), len(b.Streams))
	added := 0
	for _, es := range b.Streams {
		found := false
		for _, old := range a.Streams {
			if old.PID == es.PID {
				found = true
				break
			}
		}
		if !found {
			added++
		}
	}
	if added > 0 {
		out += fmt.Sprintf(", %d new elementary streams", added)
	}
	return out
}
