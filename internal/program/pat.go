package program

import (
	"fmt"
	"sort"

	"tsinspect/internal/psi"
)

// ApplyPAT feeds one CRC-valid PAT section. A section is only committed once
// every section_number 0..last_section_number of the same version has been
// seen and the merged result is internally consistent.
func (m *Manager) ApplyPAT(s *psi.Section) error {
	if s.TableID != psi.TableIDPAT {
		return fmt.Errorf("program: table 0x%02x is not PAT", s.TableID)
	}
	pat, err := psi.ParsePAT(s)
	if err != nil {
		return err
	}
	if !pat.CurrentNext {
		m.noteOnce(fmt.Sprintf("pat-next-%d", pat.Version), Event{
			Kind: "pat", Version: pat.Version, Status: StatusIgnoredNextTable,
			Detail: "current_next_indicator=0",
		})
		return nil
	}
	if m.pat != nil && versionRelation(m.pat.Version, pat.Version) < 0 {
		m.noteOnce(fmt.Sprintf("pat-stale-%d", pat.Version), Event{
			Kind: "pat", Version: pat.Version, Status: StatusStaleVersion,
			Detail: fmt.Sprintf("older than effective version %d", m.pat.Version),
		})
		return nil
	}

	cand := m.patPending
	if cand == nil || cand.version != pat.Version || int(cand.last) != int(pat.LastSectionNumber) {
		if cand != nil && !cand.complete {
			m.event(Event{Kind: "pat", Version: cand.version, Status: StatusPending,
				Detail: "version superseded before all sections arrived"})
		}
		cand = &patCandidate{
			version:  pat.Version,
			last:     pat.LastSectionNumber,
			sections: make([][]byte, int(pat.LastSectionNumber)+1),
		}
		m.patPending = cand
	}

	if m.pat != nil && m.pat.Version == pat.Version && m.patGeneration > 0 {
		// A same-version retransmission of an already-effective table must be
		// content-identical. Check this per section as it arrives so a bogus
		// retransmission cannot silently look like the current table.
		if err := m.checkSameVersionPATSections(pat); err != nil {
			return err
		}
	}

	cand.sections[pat.SectionNumber] = append([]byte(nil), s.Body...)
	for _, body := range cand.sections {
		if body == nil {
			return nil
		}
	}
	merged, err := mergePATSections(cand)
	if err != nil {
		m.event(Event{Kind: "pat", Version: pat.Version, Status: StatusInconsistentSections, Detail: err.Error()})
		m.patPending = nil
		return err
	}
	cand.merged, cand.complete = merged, true
	return m.commitPAT(cand)
}

func mergePATSections(c *patCandidate) (*psi.PAT, error) {
	merged := &psi.PAT{
		TransportStreamID: 0,
		Version:           c.version,
		CurrentNext:       true,
		LastSectionNumber: c.last,
	}
	seen := make(map[uint16]uint16, len(c.sections))
	for i, body := range c.sections {
		part, err := psi.ParsePAT(&psi.Section{
			TableID: psi.TableIDPAT, Extension: merged.TransportStreamID, Version: c.version,
			CurrentNext: true, SectionNumber: uint8(i), LastSectionNumber: c.last, Body: body,
		})
		if err != nil {
			return nil, fmt.Errorf("section %d: %w", i, err)
		}
		if i == 0 {
			merged.TransportStreamID = part.TransportStreamID
			merged.NetworkPID, merged.HasNetworkPID = part.NetworkPID, part.HasNetworkPID
		} else if part.TransportStreamID != merged.TransportStreamID {
			return nil, fmt.Errorf("section %d has transport_stream_id %d, want %d", i, part.TransportStreamID, merged.TransportStreamID)
		}
		for _, p := range part.Programs {
			if pid, ok := seen[p.ProgramNumber]; ok {
				if pid != p.PID {
					return nil, fmt.Errorf("program %d maps to PID %d and %d across sections", p.ProgramNumber, pid, p.PID)
				}
				continue // exact duplicate across sections is harmless
			}
			if len(seen) >= MaxPrograms {
				return nil, fmt.Errorf("PAT declares more than %d programs", MaxPrograms)
			}
			seen[p.ProgramNumber] = p.PID
			merged.Programs = append(merged.Programs, p)
		}
	}
	sort.Slice(merged.Programs, func(i, j int) bool {
		return merged.Programs[i].ProgramNumber < merged.Programs[j].ProgramNumber
	})
	// One PMT PID serving two programs would make PID->program resolution
	// depend on map order; reject the table instead of picking arbitrarily.
	pmtPIDOwner := make(map[uint16]uint16, len(merged.Programs))
	for _, p := range merged.Programs {
		if owner, ok := pmtPIDOwner[p.PID]; ok && owner != p.ProgramNumber {
			return nil, fmt.Errorf("programs %d and %d both use PMT PID %d", owner, p.ProgramNumber, p.PID)
		}
		pmtPIDOwner[p.PID] = p.ProgramNumber
	}
	return merged, nil
}

// checkSameVersionPATSections compares a newly received section against the
// effective PAT of the same version.
func (m *Manager) checkSameVersionPATSections(pat *psi.PAT) error {
	effectiveSet := make(map[uint16]uint16, len(m.pat.Programs))
	for program, pid := range m.pat.Programs {
		effectiveSet[program] = pid
	}
	for _, p := range pat.Programs {
		pid, ok := effectiveSet[p.ProgramNumber]
		if !ok {
			return &ErrConflict{Kind: "PAT", Version: pat.Version,
				Detail: fmt.Sprintf("program %d present in re-transmitted section but absent from effective table", p.ProgramNumber)}
		}
		if pid != p.PID {
			return &ErrConflict{Kind: "PAT", Version: pat.Version,
				Detail: fmt.Sprintf("program %d maps to PID %d but effective table has %d", p.ProgramNumber, p.PID, pid)}
		}
	}
	if int(pat.LastSectionNumber) == 0 && len(pat.Programs) != len(m.pat.Programs) {
		return &ErrConflict{Kind: "PAT", Version: pat.Version,
			Detail: fmt.Sprintf("re-transmitted single-section table has %d programs, effective has %d",
				len(pat.Programs), len(m.pat.Programs))}
	}
	return nil
}

func (m *Manager) commitPAT(c *patCandidate) error {
	next := &PATState{
		TransportStreamID: c.merged.TransportStreamID,
		Version:           c.version,
		Programs:          make(map[uint16]uint16, len(c.merged.Programs)),
	}
	for _, p := range c.merged.Programs {
		next.Programs[p.ProgramNumber] = p.PID
	}
	prev := m.pat
	if prev != nil && prev.Version == next.Version {
		if describePAT(prev) != describePAT(next) {
			detail := describePATDiff(prev, next)
			m.event(Event{Kind: "pat", Version: next.Version, Status: StatusSameVersionConflict, Detail: detail})
			return &ErrConflict{Kind: "PAT", Version: next.Version, Detail: detail}
		}
		// Identical retransmission: nothing changes.
		m.patPending = nil
		return nil
	}

	status, detail := StatusApplied, "initial PAT"
	if prev != nil {
		detail = describePATDiff(prev, next)
		if wrappedVersion(prev.Version, next.Version) {
			status = StatusAppliedVersionWrap
			detail += "; 5-bit version wrapped"
		}
		m.patChanges++
		m.retireRemovedPrograms(prev, next)
	}
	m.event(Event{Kind: "pat", Version: next.Version, Status: status, Detail: detail, EffectiveVersion: effective(next.Version)})
	m.pat = next
	m.patPending = nil
	m.patGeneration++
	return nil
}

// retireRemovedPrograms drops PMT state for programs the new PAT deleted or
// moved to another PMT PID. Stale streams must never be joined to new ones.
func (m *Manager) retireRemovedPrograms(prev, next *PATState) {
	for program, pid := range prev.Programs {
		newPID, still := next.Programs[program]
		if still && newPID == pid {
			continue
		}
		old := m.pmts[program]
		if old == nil {
			delete(m.pmtPending, program)
			continue
		}
		detail := "program removed by newer PAT version"
		status := StatusRemoved
		if still {
			detail = fmt.Sprintf("PMT PID changed from %d to %d", pid, newPID)
			status = StatusPMTPIDChanged
		}
		m.event(Event{Kind: "pmt", ProgramNumber: program, PID: pid, Version: old.Version, Status: status, Detail: detail})
		delete(m.pmts, program)
		delete(m.pmtPending, program)
	}
}
