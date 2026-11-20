// Package report renders analysis summaries as stable text or JSON.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"tsmap/internal/demux"
)

// WriteJSON renders the summary as indented JSON.
func WriteJSON(w io.Writer, s *demux.Summary) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}

// WriteText renders the summary as a stable, grep-friendly text report.
func WriteText(w io.Writer, s *demux.Summary) error {
	p := func(format string, args ...any) { fmt.Fprintf(w, format+"\n", args...) }
	p("input: %s", s.Input)
	p("packets_total: %d", s.PacketsTotal)
	p("pat_version_changes: %d", s.PATVersionChanges)
	pmtTotal := 0
	progs := make([]int, 0, len(s.PMTVersionChanges))
	for num := range s.PMTVersionChanges {
		progs = append(progs, int(num))
	}
	sort.Ints(progs)
	for _, num := range progs {
		pmtTotal += s.PMTVersionChanges[uint16(num)]
	}
	p("pmt_version_changes: %d", pmtTotal)
	for _, num := range progs {
		p("  program %d: %d", num, s.PMTVersionChanges[uint16(num)])
	}
	p("pid_map:")
	for _, prog := range s.Programs {
		p("  program %d: pmt_pid=%d pcr_pid=%d", prog.Number, prog.PMTPID, prog.PCRPID)
		for _, st := range prog.Streams {
			p("    es pid=%d stream_type=0x%02x", st.PID, st.StreamType)
		}
	}
	p("es_packets:")
	for _, es := range s.ES {
		line := fmt.Sprintf("  pid=%d program=%d stream_type=0x%02x es_packets=%d cc_gap_events=%d estimated_lost=%d duplicates=%d",
			es.PID, es.Program, es.StreamType, es.PayloadPackets, es.GapEvents, es.EstimatedLost, es.Duplicates)
		if es.PESCount > 0 {
			line += fmt.Sprintf(" pes=%d", es.PESCount)
		}
		if es.FirstPTS != nil {
			line += fmt.Sprintf(" first_pts=%d", *es.FirstPTS)
		}
		p("%s", line)
	}
	p("anomalies: %d", len(s.Anomalies))
	for _, a := range s.Anomalies {
		p("  offset=%d kind=%s %s", a.Offset, a.Kind, a.Detail)
	}
	if s.Fatal == "" {
		p("fatal: none")
	} else {
		p("fatal: %s", s.Fatal)
	}
	return nil
}
