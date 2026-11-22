// Package report defines the audit report data model and renders it as
// stably ordered JSON and as a terminal summary.
package report

import (
	"encoding/json"
	"fmt"
	"io"
)

// Report is the full audit result. All slices are sorted by the
// producing pipeline so the serialized form is deterministic.
type Report struct {
	Input              InputStats       `json:"input"`
	PAT                PATReport        `json:"pat"`
	PMTs               []PMTReport      `json:"pmts"`
	Intervals          []IntervalReport `json:"intervals"`
	ElementaryStreams  []StreamCount    `json:"elementary_streams"`
	StreamTotals       []TypeTotal      `json:"stream_totals"`
	Continuity         ContinuityReport `json:"continuity"`
	Unmapped           UnmappedReport   `json:"unmapped"`
	PSI                PSIReport        `json:"psi"`
}

// InputStats counts packet-level input anomalies.
type InputStats struct {
	PacketsTotal         int `json:"packets_total"`
	SyncErrors           int `json:"sync_errors"`
	TruncatedTailBytes   int `json:"truncated_tail_bytes"`
	TransportErrorPkts   int `json:"transport_error_packets"`
}

// PATReport summarizes PAT version tracking and the final program map.
type PATReport struct {
	VersionChanges int          `json:"version_changes"`
	CurrentVersion int          `json:"current_version"`
	Programs       []PATProgram `json:"programs"`
}

// PATProgram is the final program -> PMT PID mapping.
type PATProgram struct {
	Program uint16 `json:"program"`
	PMTPID  uint16 `json:"pmt_pid"`
}

// PMTReport is the final effective PMT of one program.
type PMTReport struct {
	Program        uint16       `json:"program"`
	PMTPID         uint16       `json:"pmt_pid"`
	Version        int          `json:"version"`
	VersionChanges int          `json:"version_changes"`
	Streams        []StreamDesc `json:"streams"`
}

// StreamDesc describes an elementary stream without packet counts.
type StreamDesc struct {
	PID        uint16 `json:"pid"`
	StreamType uint8  `json:"stream_type"`
	Type       string `json:"type"`
}

// StreamCount adds a packet count to a stream description.
type StreamCount struct {
	StreamDesc
	Packets int `json:"packets"`
}

// ProgramCount holds per-stream counts of one program in an interval.
type ProgramCount struct {
	Program    uint16        `json:"program"`
	PMTPID     uint16        `json:"pmt_pid"`
	PMTVersion int           `json:"pmt_version"`
	Streams    []StreamCount `json:"streams"`
}

// IntervalReport covers the span governed by one effective snapshot.
type IntervalReport struct {
	Index       int            `json:"index"`
	PATVersion  int            `json:"pat_version"`
	StartPacket int            `json:"start_packet"`
	EndPacket   int            `json:"end_packet"`
	Programs    []ProgramCount `json:"programs"`
}

// TypeTotal aggregates received packets across intervals by stream type.
type TypeTotal struct {
	Type    string `json:"type"`
	Packets int    `json:"packets"`
}

// ContinuityReport summarizes continuity-counter anomalies.
type ContinuityReport struct {
	DuplicatePackets    int             `json:"duplicate_packets"`
	MissingPackets      int             `json:"missing_packets"`
	DiscontinuityEvents int             `json:"discontinuity_events"`
	PerPID              []PIDContinuity `json:"per_pid"`
}

// PIDContinuity holds continuity anomalies of one PID.
type PIDContinuity struct {
	PID             uint16 `json:"pid"`
	Duplicates      int    `json:"duplicates"`
	Missing         int    `json:"missing"`
	Discontinuities int    `json:"discontinuities"`
}

// UnmappedReport counts packets on PIDs no effective snapshot claimed.
type UnmappedReport struct {
	Packets int          `json:"packets"`
	PIDs    []PIDPackets `json:"pids"`
}

// PIDPackets counts packets seen on one PID.
type PIDPackets struct {
	PID     uint16 `json:"pid"`
	Packets int    `json:"packets"`
}

// PSIReport counts PSI-level anomalies.
type PSIReport struct {
	CRCErrors int `json:"crc_errors"`
}

// WriteJSON serializes the report with stable key and slice ordering.
func WriteJSON(w io.Writer, r *Report) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", b)
	return err
}

// PrintSummary writes a compact human-readable summary.
func PrintSummary(w io.Writer, r *Report) {
	fmt.Fprintf(w, "packets: %d received, %d corrupt skipped, %d transport-error\n",
		r.Input.PacketsTotal, r.Input.SyncErrors, r.Input.TransportErrorPkts)
	fmt.Fprintf(w, "PAT: version_changes=%d current_version=%d\n",
		r.PAT.VersionChanges, r.PAT.CurrentVersion)
	for _, p := range r.PAT.Programs {
		fmt.Fprintf(w, "  program %d -> PMT PID %d\n", p.Program, p.PMTPID)
	}
	for _, m := range r.PMTs {
		fmt.Fprintf(w, "PMT pid=%d program=%d version=%d version_changes=%d\n",
			m.PMTPID, m.Program, m.Version, m.VersionChanges)
		for _, s := range m.Streams {
			fmt.Fprintf(w, "  stream pid=%d type=%s\n", s.PID, s.Type)
		}
	}
	for _, t := range r.StreamTotals {
		fmt.Fprintf(w, "stream total: %s %d packets\n", t.Type, t.Packets)
	}
	fmt.Fprintf(w, "continuity: duplicates=%d missing=%d discontinuities=%d\n",
		r.Continuity.DuplicatePackets, r.Continuity.MissingPackets,
		r.Continuity.DiscontinuityEvents)
	fmt.Fprintf(w, "unmapped: %d packets; psi crc errors: %d\n",
		r.Unmapped.Packets, r.PSI.CRCErrors)
}
