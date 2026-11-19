// Package report renders a deterministic JSON summary of one inspection pass.
// Every list is sorted explicitly; the struct field order fixes key order.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"tsinspect/internal/demux"
)

// Build assembles the summary from a pipeline result.
func Build(inputName string, res *demux.Result, slice *demux.SliceStats, extraErrors []string) *Summary {
	s := &Summary{
		Input:             inputName,
		PacketsProcessed:  res.Packets,
		BytesProcessed:    res.Bytes,
		PATVersionChanges: res.Programs.PATVersionChanges(),
		PMTVersionChanges: res.Programs.PMTVersionChanges(),
		Errors:            append([]string{}, extraErrors...),
	}

	for _, ev := range res.Programs.Events() {
		s.TableEvents = append(s.TableEvents, TableEvent{
			Kind: ev.Kind, ProgramNumber: ev.ProgramNumber, PID: ev.PID,
			Version: ev.Version, Status: ev.Status, Detail: ev.Detail,
		})
	}
	if s.TableEvents == nil {
		s.TableEvents = []TableEvent{}
	}

	for _, num := range res.Programs.Programs() {
		pmt := res.Programs.PMT(num)
		pr := ProgramReport{ProgramNumber: num, PMTPID: pmt.PID, PCRPID: pmt.PCRPID, Version: pmt.Version}
		for _, es := range pmt.Streams {
			pr.StreamPIDs = append(pr.StreamPIDs, es.PID)
		}
		sort.Slice(pr.StreamPIDs, func(i, j int) bool { return pr.StreamPIDs[i] < pr.StreamPIDs[j] })
		s.Programs = append(s.Programs, pr)

		for _, es := range pmt.Streams {
			count, seen := packetAccounting(res, es.PID)
			s.PIDMappings = append(s.PIDMappings, PIDMapping{
				ProgramNumber: num, PID: es.PID, StreamType: es.StreamType,
				PacketCount: count, DiscontinuitySeen: seen,
			})
			s.ElementaryStreams = append(s.ElementaryStreams, ElementaryStream{
				ProgramNumber: num, PID: es.PID, StreamType: es.StreamType,
				PacketCount: count, DiscontinuitySeen: seen,
			})
		}
	}
	sortMappings(s.PIDMappings)
	sort.Slice(s.ElementaryStreams, func(i, j int) bool {
		a, b := s.ElementaryStreams[i], s.ElementaryStreams[j]
		if a.ProgramNumber != b.ProgramNumber {
			return a.ProgramNumber < b.ProgramNumber
		}
		return a.PID < b.PID
	})

	tot := res.Metrics.Totals()
	s.Anomalies = Anomalies{
		Duplicates: tot.Duplicates, Jumps: tot.Jumps,
		TransportErrors: tot.TransportErrors, AFOnly: tot.AFOnly,
		UnknownGaps: tot.UnknownGaps, PIDsDiscontinuous: tot.Discontinuities,
		CRCErrors: res.CRCErrors, SectionErrors: res.SectionErrors,
		OrphanPMTs: int64(res.Programs.OrphanPMTs()), IgnoredTables: res.IgnoredTables,
	}

	if slice != nil && slice.Enabled {
		s.Slice = &SliceReport{
			Program: slice.Program, PacketsWritten: slice.PacketsWritten,
			PMTPackets: slice.PMTPackets, ESPackets: slice.ESPackets,
			BufferedFlushed: slice.BufferedFlushed, BufferedDropped: slice.BufferedDropped,
			BufferOverflows: slice.BufferOverflows, ProgramEnded: slice.ProgramEnded,
			ProgramAbsent: slice.ProgramAbsent, BoundaryCount: len(slice.Boundaries),
			Boundaries: append([]demux.Boundary{}, slice.Boundaries...),
		}
	}

	if s.PIDMappings == nil {
		s.PIDMappings = []PIDMapping{}
	}
	if s.ElementaryStreams == nil {
		s.ElementaryStreams = []ElementaryStream{}
	}
	if s.Programs == nil {
		s.Programs = []ProgramReport{}
	}
	return s
}

func packetAccounting(res *demux.Result, pid uint16) (int64, bool) {
	if st, ok := res.Metrics.Get(pid); ok {
		return st.Packets, st.DiscontinuitySeen
	}
	return 0, false
}

func sortMappings(rows []PIDMapping) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].ProgramNumber != rows[j].ProgramNumber {
			return rows[i].ProgramNumber < rows[j].ProgramNumber
		}
		return rows[i].PID < rows[j].PID
	})
}

// WriteJSON writes the summary with a trailing newline.
func WriteJSON(w io.Writer, s *Summary) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
		return fmt.Errorf("report: encoding JSON: %w", err)
	}
	return nil
}
