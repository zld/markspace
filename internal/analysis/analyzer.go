package analysis

import (
	"io"
	"sort"

	"tsaudit/internal/program"
	"tsaudit/internal/psi"
	"tsaudit/internal/report"
	"tsaudit/internal/transport"
)

// Analyze runs the full audit pipeline over one input stream: transport
// decoding, continuity tracking, PSI assembly, catalog updates and
// per-snapshot packet accounting. Structural input damage aborts the
// analysis with transport.ErrCorrupt unless tolerant is set.
func Analyze(r io.Reader, tolerant bool) (*report.Report, error) {
	rd := transport.NewReader(r, tolerant)
	ct := transport.NewContinuityTracker()
	cat := program.NewCatalog()
	counter := NewCounter()
	tracker := psi.NewTracker()
	patAsm := &psi.Assembler{}
	pmtAsms := map[uint16]*psi.Assembler{}
	crcErrors := 0

	applyEffective := func(nextPacket int) {
		if snap, changed := cat.Effective(); changed {
			counter.Rotate(snap, nextPacket)
		}
	}

	for {
		pkt, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if pkt.TransportError {
			continue // counted in input stats; payload cannot be trusted
		}
		ct.Observe(pkt)
		switch {
		case pkt.PID == transport.NullPID:
			// padding, ignore
		case pkt.PID == 0:
			secs, errs := patAsm.Feed(pkt.PayloadUnitStart, pkt.Payload)
			crcErrors += errs
			for _, s := range secs {
				if s.TableID != psi.TableIDPAT {
					continue
				}
				t := tracker.Add(pkt.PID, s)
				if t == nil || !t.NewVersion {
					continue
				}
				entries, err := psi.ParsePAT(t)
				if err != nil {
					continue
				}
				cat.ApplyPAT(t.Version, entries)
				applyEffective(pkt.Index + 1)
				for _, pid := range cat.PMTPIDs() {
					if pmtAsms[pid] == nil {
						pmtAsms[pid] = &psi.Assembler{}
					}
				}
			}
		case pmtAsms[pkt.PID] != nil:
			secs, errs := pmtAsms[pkt.PID].Feed(pkt.PayloadUnitStart, pkt.Payload)
			crcErrors += errs
			for _, s := range secs {
				if s.TableID != psi.TableIDPMT {
					continue
				}
				t := tracker.Add(pkt.PID, s)
				if t == nil || !t.NewVersion {
					continue
				}
				_, streams, err := psi.ParsePMT(t)
				if err != nil {
					continue
				}
				cat.ApplyPMT(pkt.PID, t.Version, streams)
				applyEffective(pkt.Index + 1)
			}
		default:
			counter.Count(pkt.PID)
		}
	}
	stats := rd.Stats()
	counter.Close(stats.Packets - 1)
	return buildReport(stats, ct, cat, counter, crcErrors), nil
}

func buildReport(st transport.Stats, ct *transport.ContinuityTracker,
	cat *program.Catalog, counter *Counter, crcErrors int) *report.Report {
	rep := &report.Report{
		Input: report.InputStats{
			PacketsTotal:       st.Packets,
			SyncErrors:         st.SyncErrors,
			TruncatedTailBytes: st.TruncatedTailBytes,
			TransportErrorPkts: st.TransportErrors,
		},
		PSI:               report.PSIReport{CRCErrors: crcErrors},
		Intervals:         []report.IntervalReport{},
		ElementaryStreams: []report.StreamCount{},
		StreamTotals:      []report.TypeTotal{},
		Unmapped:          report.UnmappedReport{PIDs: []report.PIDPackets{}},
		Continuity:        report.ContinuityReport{PerPID: []report.PIDContinuity{}},
	}
	// Final PAT mapping and version tracking.
	snap := cat.Current()
	rep.PAT.VersionChanges = cat.PATVersionChanges
	if snap != nil {
		rep.PAT.CurrentVersion = snap.PATVersion
	} else {
		rep.PAT.CurrentVersion = -1
	}
	// Final effective PMTs and per-program version changes.
	for _, ps := range currentPrograms(snap) {
		rep.PAT.Programs = append(rep.PAT.Programs,
			report.PATProgram{Program: ps.Program, PMTPID: ps.PMTPID})
		m := report.PMTReport{
			Program:        ps.Program,
			PMTPID:         ps.PMTPID,
			Version:        ps.PMTVersion,
			VersionChanges: cat.PMTVersionChanges[ps.Program],
		}
		for _, s := range ps.Streams {
			m.Streams = append(m.Streams, report.StreamDesc{
				PID:        s.PID,
				StreamType: s.StreamType,
				Type:       program.StreamTypeName(s.StreamType),
			})
		}
		rep.PMTs = append(rep.PMTs, m)
	}
	buildIntervals(rep, counter)
	buildContinuity(rep, ct)
	buildUnmapped(rep, counter)
	return rep
}

func currentPrograms(snap *program.Snapshot) []program.ProgramState {
	if snap == nil {
		return nil
	}
	return snap.Programs
}

// buildIntervals converts non-empty accounting intervals and aggregates
// per-PID and per-type totals across them.
func buildIntervals(rep *report.Report, counter *Counter) {
	byPID := map[uint16]*report.StreamCount{}
	byType := map[string]int{}
	for _, iv := range counter.Intervals {
		if iv.Packets() == 0 {
			continue // snapshot never governed a received packet
		}
		riv := report.IntervalReport{
			Index:       len(rep.Intervals),
			PATVersion:  iv.PATVersion,
			StartPacket: iv.StartPacket,
			EndPacket:   iv.EndPacket,
		}
		for _, pc := range iv.Programs {
			rpc := report.ProgramCount{
				Program:    pc.Program,
				PMTPID:     pc.PMTPID,
				PMTVersion: pc.PMTVersion,
			}
			for _, sc := range pc.Streams {
				rpc.Streams = append(rpc.Streams, report.StreamCount{
					StreamDesc: report.StreamDesc{
						PID:        sc.PID,
						StreamType: sc.StreamType,
						Type:       sc.TypeName,
					},
					Packets: sc.Packets,
				})
				agg := byPID[sc.PID]
				if agg == nil {
					agg = &report.StreamCount{StreamDesc: report.StreamDesc{
						PID:        sc.PID,
						StreamType: sc.StreamType,
						Type:       sc.TypeName,
					}}
					byPID[sc.PID] = agg
				}
				agg.Packets += sc.Packets
				byType[sc.TypeName] += sc.Packets
			}
			riv.Programs = append(riv.Programs, rpc)
		}
		rep.Intervals = append(rep.Intervals, riv)
	}
	pids := make([]int, 0, len(byPID))
	for pid := range byPID {
		pids = append(pids, int(pid))
	}
	sort.Ints(pids)
	for _, pid := range pids {
		rep.ElementaryStreams = append(rep.ElementaryStreams, *byPID[uint16(pid)])
	}
	types := make([]string, 0, len(byType))
	for ty := range byType {
		types = append(types, ty)
	}
	sort.Strings(types)
	for _, ty := range types {
		rep.StreamTotals = append(rep.StreamTotals, report.TypeTotal{Type: ty, Packets: byType[ty]})
	}
}

func buildContinuity(rep *report.Report, ct *transport.ContinuityTracker) {
	rep.Continuity.DuplicatePackets = ct.Totals.Duplicates
	rep.Continuity.MissingPackets = ct.Totals.Missing
	rep.Continuity.DiscontinuityEvents = ct.Totals.Discontinuities
	pids := make([]int, 0, len(ct.ByPID))
	for pid := range ct.ByPID {
		pids = append(pids, int(pid))
	}
	sort.Ints(pids)
	for _, pid := range pids {
		s := ct.ByPID[uint16(pid)]
		rep.Continuity.PerPID = append(rep.Continuity.PerPID, report.PIDContinuity{
			PID:             uint16(pid),
			Duplicates:      s.Duplicates,
			Missing:         s.Missing,
			Discontinuities: s.Discontinuities,
		})
	}
}

func buildUnmapped(rep *report.Report, counter *Counter) {
	rep.Unmapped.Packets = counter.UnmappedPackets
	pids := make([]int, 0, len(counter.UnmappedByPID))
	for pid := range counter.UnmappedByPID {
		pids = append(pids, int(pid))
	}
	sort.Ints(pids)
	for _, pid := range pids {
		rep.Unmapped.PIDs = append(rep.Unmapped.PIDs,
			report.PIDPackets{PID: uint16(pid), Packets: counter.UnmappedByPID[uint16(pid)]})
	}
}
