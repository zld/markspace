// Package demux drives the analysis pipeline: TS reader -> continuity
// tracker, PSI assembler and table tracker, plus a go-astits pass for
// PES/PTS extraction.
package demux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/asticode/go-astits"

	"tsmap/internal/continuity"
	"tsmap/internal/psi"
	"tsmap/internal/ts"
)

// Fatal categories, mapped to process exit codes by the CLI.
const (
	FatalNone     = ""
	FatalResync   = "resync_failed"   // cannot resynchronize / structural damage
	FatalNoTables = "no_valid_tables" // EOF without a complete program table
	FatalIO       = "io_error"        // file read/write failure
)

// Anomaly is a non-fatal observation tied to a file offset.
type Anomaly struct {
	Offset int64  `json:"offset"`
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// ESStats summarizes one elementary stream.
type ESStats struct {
	PID            uint16 `json:"pid"`
	Program        uint16 `json:"program"`
	StreamType     byte   `json:"stream_type"`
	PayloadPackets uint64 `json:"payload_packets"`
	GapEvents      uint64 `json:"cc_gap_events"`
	EstimatedLost  uint64 `json:"estimated_lost"`
	Duplicates     uint64 `json:"duplicates"`
	PESCount       uint64 `json:"pes_count,omitempty"`
	FirstPTS       *int64 `json:"first_pts,omitempty"`
}

// ProgramInfo is the reported view of one program.
type ProgramInfo struct {
	Number   uint16       `json:"number"`
	PMTPID   uint16       `json:"pmt_pid"`
	PCRPID   uint16       `json:"pcr_pid"`
	Streams  []psi.Stream `json:"streams"`
}

// Summary is the full analysis result.
type Summary struct {
	Input              string         `json:"input"`
	PacketsTotal       uint64         `json:"packets_total"`
	PATVersionChanges  int            `json:"pat_version_changes"`
	PMTVersionChanges  map[uint16]int `json:"pmt_version_changes"`
	Programs           []ProgramInfo  `json:"programs"`
	ES                 []ESStats      `json:"es"`
	Anomalies          []Anomaly      `json:"anomalies"`
	Fatal              string         `json:"fatal,omitempty"`
	HasCompleteTables  bool           `json:"has_complete_tables"`
}

// Analyzer folds packets into continuity and PSI state.
type Analyzer struct {
	tracker   *continuity.Tracker
	asm       *psi.Assembler
	tables    *psi.TableTracker
	anomalies []Anomaly
	scrambled map[uint16]bool
	packets   uint64
}

// New returns an empty Analyzer.
func New() *Analyzer {
	return &Analyzer{
		tracker:   continuity.New(),
		asm:       psi.NewAssembler(),
		tables:    psi.NewTableTracker(),
		scrambled: make(map[uint16]bool),
	}
}

// Tables exposes the committed table state (used by the slicer).
func (a *Analyzer) Tables() *psi.TableTracker { return a.tables }

// Process dispatches one packet to the sub-trackers.
func (a *Analyzer) Process(p *ts.Packet) {
	a.packets++
	a.tracker.Observe(p)
	if p.TEI {
		a.anomalies = append(a.anomalies, Anomaly{p.Offset, "tei",
			fmt.Sprintf("transport_error_indicator set on PID %d", p.PID)})
	}
	if p.ScramblingControl != 0 {
		if !a.scrambled[p.PID] {
			a.scrambled[p.PID] = true
			a.anomalies = append(a.anomalies, Anomaly{p.Offset, "scrambled",
				fmt.Sprintf("encrypted payload on PID %d is not supported", p.PID)})
		}
		return
	}
	if a.isPSIPID(p.PID) {
		sections, err := a.asm.Feed(p)
		if err != nil {
			a.anomalies = append(a.anomalies, Anomaly{p.Offset, "invalid_psi", err.Error()})
			return
		}
		for _, s := range sections {
			if err := a.tables.Feed(s); err != nil {
				a.anomalies = append(a.anomalies, Anomaly{s.Offset, "invalid_psi", err.Error()})
			}
		}
	}
}

func (a *Analyzer) isPSIPID(pid uint16) bool {
	if pid == 0 {
		return true
	}
	for _, prog := range a.tables.Current().Programs {
		if prog.PMTPID == pid {
			return true
		}
	}
	return false
}

// Summary builds the final report. pes comes from the astits pass.
func (a *Analyzer) Summary(input string, pes map[uint16]*PESInfo) *Summary {
	s := &Summary{
		Input:             input,
		PacketsTotal:      a.packets,
		PMTVersionChanges: make(map[uint16]int),
	}
	snap := a.tables.Current()
	s.HasCompleteTables = snap.Complete()
	s.PATVersionChanges = a.tables.Stats.PATChanges
	for prog, n := range a.tables.Stats.PMTChanges {
		s.PMTVersionChanges[prog] = n
	}
	for _, ev := range a.tracker.Events() {
		kind := "cc_gap"
		detail := fmt.Sprintf("PID %d: expected CC %d, got %d (lost ~%d)", ev.PID, ev.Expected, ev.Got, ev.Lost)
		switch ev.Kind {
		case continuity.EventDuplicate:
			kind = "duplicate"
			detail = fmt.Sprintf("PID %d: duplicate packet (CC %d)", ev.PID, ev.Got)
		case continuity.EventDiscontinuity:
			kind = "discontinuity"
			detail = fmt.Sprintf("PID %d: discontinuity_indicator, CC baseline reset", ev.PID)
		}
		a.anomalies = append(a.anomalies, Anomaly{ev.Offset, kind, detail})
	}
	s.Anomalies = a.anomalies
	progs := sortedPrograms(snap)
	for _, p := range progs {
		if !p.HasPMT {
			continue
		}
		s.Programs = append(s.Programs, ProgramInfo{
			Number: p.Number, PMTPID: p.PMTPID, PCRPID: p.PCRPID, Streams: p.Streams,
		})
		for _, st := range p.Streams {
			cs := a.tracker.StatsFor(st.PID)
			es := ESStats{
				PID: st.PID, Program: p.Number, StreamType: st.StreamType,
				PayloadPackets: cs.PayloadPackets, GapEvents: cs.GapEvents,
				EstimatedLost: cs.EstimatedLost, Duplicates: cs.Duplicates,
			}
			if pi := pes[st.PID]; pi != nil {
				es.PESCount = pi.Count
				es.FirstPTS = pi.FirstPTS
			}
			s.ES = append(s.ES, es)
		}
	}
	return s
}

func sortedPrograms(snap *psi.Snapshot) []*psi.ProgramSnapshot {
	out := make([]*psi.ProgramSnapshot, 0, len(snap.Programs))
	for _, p := range snap.Programs {
		out = append(out, p)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Number < out[j-1].Number; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// PESInfo aggregates the astits PES pass for one PID.
type PESInfo struct {
	Count    uint64
	FirstPTS *int64
}

// CollectPES runs a go-astits demuxer pass over path to extract PES counts
// and first PTS per PID. A parse failure returns the partial map plus the
// error; callers decide how to classify it.
func CollectPES(path string) (map[uint16]*PESInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info := make(map[uint16]*PESInfo)
	dmx := astits.NewDemuxer(context.Background(), f)
	for {
		d, err := dmx.NextData()
		if err != nil {
			if errors.Is(err, astits.ErrNoMorePackets) {
				return info, nil
			}
			return info, err
		}
		if d.PES == nil {
			continue
		}
		pi := info[d.PID]
		if pi == nil {
			pi = &PESInfo{}
			info[d.PID] = pi
		}
		pi.Count++
		if pi.FirstPTS == nil && d.PES.Header.OptionalHeader != nil &&
			d.PES.Header.OptionalHeader.PTS != nil {
			v := d.PES.Header.OptionalHeader.PTS.Base
			pi.FirstPTS = &v
		}
	}
}

// AnalyzeFile runs the full pipeline over path.
func AnalyzeFile(path string) (*Summary, error) {
	a := New()
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", FatalIO, err)
	}
	defer f.Close()
	r := ts.NewReader(f)
	fatal := ""
	for {
		pkt, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			fatal = FatalResync
			a.anomalies = append(a.anomalies, Anomaly{offsetOf(err), "fatal", err.Error()})
			break
		}
		a.Process(pkt)
	}
	if r.Trailing > 0 {
		a.anomalies = append(a.anomalies, Anomaly{-1, "trailing_bytes",
			fmt.Sprintf("%d trailing bytes after last full packet", r.Trailing)})
	}
	pes, pesErr := CollectPES(path)
	if pesErr != nil {
		a.anomalies = append(a.anomalies, Anomaly{-1, "pes_parse", pesErr.Error()})
	}
	s := a.Summary(path, pes)
	s.Fatal = fatal
	if fatal == "" && !s.HasCompleteTables {
		s.Fatal = FatalNoTables
	}
	return s, nil
}

func offsetOf(err error) int64 {
	var re *ts.ResyncError
	if errors.As(err, &re) {
		return re.Offset
	}
	return -1
}
