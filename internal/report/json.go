// Package report renders the rebuilt mapping timeline, per-PID statistics and
// segmenter accounting as stable JSON. Field order is fixed by the structs and
// every slice is sorted at the source, so identical input always yields
// byte-identical output.
package report

import (
	"encoding/json"
	"io"

	"tsrebuild/internal/inspect"
	"tsrebuild/internal/program"
	"tsrebuild/internal/segmenter"
)

// StreamReport is the top-level inspect document.
type StreamReport struct {
	TransportStreamID   uint16            `json:"transport_stream_id"`
	ProgramNumber       uint16            `json:"program_number"`
	TotalPackets        int64             `json:"total_packets"`
	PATVersionChanges   int               `json:"pat_version_changes"`
	PMTVersionChanges   int               `json:"pmt_version_changes"`
	RejectedCRC         RejectedCRC       `json:"rejected_crc"`
	MappingIntervals    []MappingInterval `json:"mapping_intervals"`
	PIDStats            []PIDStatsJSON    `json:"pid_stats"`
	PerIntervalPIDStats [][]PIDStatsJSON  `json:"per_interval_pid_stats"`
}

// RejectedCRC counts CRC-invalid PSI sections by table.
type RejectedCRC struct {
	PAT int `json:"pat"`
	PMT int `json:"pmt"`
}

// MappingInterval is the JSON form of a program mapping interval.
type MappingInterval struct {
	StartPacket int64        `json:"start_packet"`
	EndPacket   int64        `json:"end_packet"` // -1 while open
	PATVersion  uint8        `json:"pat_version"`
	PMTVersion  uint8        `json:"pmt_version"`
	PMTPID      uint16       `json:"pmt_pid"`
	PCRPID      uint16       `json:"pcr_pid"`
	Streams     []StreamJSON `json:"streams"`
}

// StreamJSON describes one elementary stream inside a mapping.
type StreamJSON struct {
	PID        uint16 `json:"pid"`
	StreamType uint8  `json:"stream_type"`
	Kind       string `json:"kind"`
}

// PIDStatsJSON is the JSON form of per-PID counters.
type PIDStatsJSON struct {
	PID               uint16 `json:"pid"`
	Kind              string `json:"kind"`
	IntervalStart     int64  `json:"interval_start"`
	IntervalEnd       int64  `json:"interval_end"`
	Packets           int64  `json:"packets"`
	PayloadPackets    int64  `json:"payload_packets"`
	ContinuityGaps    int64  `json:"continuity_gaps"`
	DuplicateCounters int64  `json:"duplicate_counters"`
	TransportErrors   int64  `json:"transport_errors"`
}

// SegmentReport is the JSON form of the segmenter's accounting.
type SegmentReport struct {
	ProgramNumber  uint16                `json:"program_number"`
	InputPackets   int64                 `json:"input_packets"`
	WrittenPackets int64                 `json:"written_packets"`
	PATPackets     int64                 `json:"pat_packets"`
	Intervals      []SegmentIntervalJSON `json:"intervals"`
}

// SegmentIntervalJSON reports per-interval emitted PID counts.
type SegmentIntervalJSON struct {
	StartPacket int64         `json:"start_packet"`
	EndPacket   int64         `json:"end_packet"`
	PATVersion  uint8         `json:"pat_version"`
	PMTVersion  uint8         `json:"pmt_version"`
	PMTPID      uint16        `json:"pmt_pid"`
	Written     []WrittenJSON `json:"written"`
}

// WrittenJSON is one PID's emitted tally.
type WrittenJSON struct {
	PID     uint16 `json:"pid"`
	Kind    string `json:"kind"`
	Packets int64  `json:"packets"`
}

// BuildInspect assembles the inspect document from the timeline and stats.
func BuildInspect(tl *program.Timeline, stats inspect.Result, totalPackets int64,
	rejectedPAT, rejectedPMT int) StreamReport {

	intervals := tl.Intervals(stats.ProgramNumber)
	out := StreamReport{
		TransportStreamID: tl.TransportStreamID(),
		ProgramNumber:     stats.ProgramNumber,
		TotalPackets:      totalPackets,
		PATVersionChanges: tl.PATVersionChanges(),
		PMTVersionChanges: tl.PMTVersionChanges(stats.ProgramNumber),
		RejectedCRC:       RejectedCRC{PAT: rejectedPAT, PMT: rejectedPMT},
		MappingIntervals:  make([]MappingInterval, 0, len(intervals)),
		PIDStats:          []PIDStatsJSON{},
	}
	for _, iv := range intervals {
		mj := MappingInterval{
			StartPacket: iv.StartPacket,
			EndPacket:   iv.EndPacket,
			PATVersion:  iv.Mapping.PATVersion,
			PMTVersion:  iv.Mapping.Version,
			PMTPID:      iv.Mapping.PMTPID,
			PCRPID:      iv.Mapping.PCRPID,
			Streams:     make([]StreamJSON, 0, len(iv.Mapping.Streams)),
		}
		for _, s := range iv.Mapping.Streams {
			mj.Streams = append(mj.Streams, StreamJSON{
				PID: s.PID, StreamType: s.StreamType, Kind: string(s.Kind),
			})
		}
		out.MappingIntervals = append(out.MappingIntervals, mj)
	}

	// One entry per PID per mapping interval, kept in stream/PID order so every
	// counter can be traced back to the packet range it was attributed to.
	for _, group := range stats.Intervals {
		jg := make([]PIDStatsJSON, 0, len(group))
		for _, s := range group {
			j := toPIDStatsJSON(s)
			jg = append(jg, j)
			out.PIDStats = append(out.PIDStats, j)
		}
		out.PerIntervalPIDStats = append(out.PerIntervalPIDStats, jg)
	}
	// PAT counters are global; surface them as the first PID entry.
	pat := toPIDStatsJSON(stats.PAT)
	out.PIDStats = append([]PIDStatsJSON{pat}, out.PIDStats...)
	return out
}

func toPIDStatsJSON(s inspect.PIDStats) PIDStatsJSON {
	return PIDStatsJSON{
		PID: s.PID, Kind: string(s.Kind),
		IntervalStart:     s.IntervalStart,
		IntervalEnd:       s.IntervalEnd,
		Packets:           s.Packets,
		PayloadPackets:    s.PayloadPackets,
		ContinuityGaps:    s.ContinuityGaps,
		DuplicateCounters: s.DuplicateCounters,
		TransportErrors:   s.TransportErrors,
	}
}

// BuildSegment assembles the segment document.
func BuildSegment(rep *segmenter.Report) SegmentReport {
	out := SegmentReport{
		ProgramNumber:  rep.ProgramNumber,
		InputPackets:   rep.InputPackets,
		WrittenPackets: rep.WrittenPackets,
		PATPackets:     rep.PATPackets,
		Intervals:      make([]SegmentIntervalJSON, 0, len(rep.Intervals)),
	}
	for _, iv := range rep.Intervals {
		jiv := SegmentIntervalJSON{
			StartPacket: iv.StartPacket, EndPacket: iv.EndPacket,
			PATVersion: iv.PATVersion, PMTVersion: iv.PMTVersion, PMTPID: iv.PMTPID,
			Written: make([]WrittenJSON, 0, len(iv.Written)),
		}
		for _, c := range iv.Written {
			jiv.Written = append(jiv.Written, WrittenJSON{PID: c.PID, Kind: c.Kind, Packets: c.Packets})
		}
		out.Intervals = append(out.Intervals, jiv)
	}
	return out
}

// WriteJSON encodes v as indented JSON with a trailing newline.
func WriteJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
