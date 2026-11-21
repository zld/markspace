package analyze

import (
	"sort"

	"github.com/example/tsaudit/internal/psi"
)

// buildSummary consolidates pipeline state into the stable report structure.
func (a *Analyzer) buildSummary(strict bool) *Summary {
	a.progs.CloseIntervals(a.transport.TotalPackets - 1)

	stats := a.assembler.Stats()
	patChanges, pmtChanges, patWraps, pmtWraps := 0, 0, 0, 0
	for _, s := range stats {
		switch s.TableID {
		case psi.TableIDPAT:
			patChanges, patWraps = s.VersionChanges, s.Wraps
		case psi.TableIDPMT:
			pmtChanges += s.VersionChanges
			pmtWraps += s.Wraps
		}
	}

	completePAT := false
	for _, pub := range a.assembler.Publications {
		if pub.TableID == psi.TableIDPAT {
			completePAT = true
			break
		}
	}

	missing := a.missingPMTs()
	counters := a.buildCounters()

	sum := &Summary{
		Source:            a.source,
		Transport:         a.transport,
		PATVersionChanges: patChanges,
		PMTVersionChanges: pmtChanges,
		PATWraps:          patWraps,
		PMTWraps:          pmtWraps,
		PublishedVersions: append([]psi.Publication(nil), a.assembler.Publications...),
		Mappings:          a.progs.Records(),
		PIDCounters:       counters,
		CorruptSections:   append([]psi.SectionCandidate(nil), a.assembler.Corrupt...),
		HasCompletePAT:    completePAT,
		MissingPMTs:       missing,
		StructuralErrors:  append([]string(nil), a.errs...),
	}
	sum.ExitCode = a.deriveExitCode(sum, strict)
	return sum
}

// missingPMTs lists program numbers whose PAT references a PMT version that
// was never completely published, in ascending order.
func (a *Analyzer) missingPMTs() []uint16 {
	pmtPrograms := map[uint16]bool{}
	for _, pub := range a.assembler.Publications {
		if pub.TableID == psi.TableIDPMT {
			pmtPrograms[pub.PMT.ProgramNumber] = true
		}
	}
	var missing []uint16
	for program := range a.progs.PATPrograms {
		if !pmtPrograms[program] {
			missing = append(missing, program)
		}
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
	return missing
}

// buildCounters joins raw packet counts with continuity statistics, ordered by
// PID. PSI PIDs are included so the slicer's packet accounting is auditable.
func (a *Analyzer) buildCounters() []PIDCounters {
	cc := map[uint16]struct {
		valid, dup, gaps, events, errs int
	}{}
	for _, s := range a.tracker.Stats() {
		cc[s.PID] = struct {
			valid, dup, gaps, events, errs int
		}{s.ValidPackets, s.Duplicates, s.GapPackets, s.GapEvents, s.ErrorPackets}
	}

	pidSet := map[uint16]bool{}
	for pid := range a.rawPID {
		pidSet[pid] = true
	}
	pids := make([]uint16, 0, len(pidSet))
	for pid := range pidSet {
		pids = append(pids, pid)
	}
	sort.Slice(pids, func(i, j int) bool { return pids[i] < pids[j] })

	out := make([]PIDCounters, 0, len(pids))
	for _, pid := range pids {
		c := PIDCounters{PID: pid, TotalPackets: a.rawPID[pid]}
		if v, ok := cc[pid]; ok {
			c.ValidPackets, c.Duplicates = v.valid, v.dup
			c.ContinuityGaps, c.GapEvents = v.gaps, v.events
			c.ErrorPackets = v.errs
		}
		out = append(out, c)
	}
	return out
}

// deriveExitCode encodes the CLI contract: structural problems dominate,
// strict continuity failures take precedence over an otherwise clean run.
func (a *Analyzer) deriveExitCode(sum *Summary, strict bool) int {
	structural := !sum.HasCompletePAT ||
		len(sum.MissingPMTs) > 0 ||
		sum.Transport.SyncErrors > 0 ||
		sum.Transport.TruncatedTailBytes > 0 ||
		len(sum.CorruptSections) > 0
	if structural {
		return ExitStructural
	}
	if strict && a.tracker.TotalGapPackets() > 0 {
		return ExitStrictContinuity
	}
	return ExitOK
}
