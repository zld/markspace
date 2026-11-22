// Package report renders the pipeline result as a JSON document (for
// recomputable evidence) and as a terminal summary.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"tsaudit/internal/psi"
	"tsaudit/internal/slicer"
)

// Status values of a run.
const (
	StatusOK        = "ok"
	StatusNoTables  = "no_valid_tables"
)

// StreamInfo describes one mapped elementary stream.
type StreamInfo struct {
	Program        uint16 `json:"program"`
	PID            uint16 `json:"pid"`
	StreamType     uint8  `json:"stream_type"`
	StreamTypeName string `json:"stream_type_name"`
}

// IntervalMap is the PID mapping of one interval.
type IntervalMap struct {
	Interval        int           `json:"interval"`
	EffectivePacket int           `json:"effective_packet"`
	PATVersion      int           `json:"pat_version"`
	PMTVersions     map[uint16]int `json:"pmt_versions"`
	Streams         []StreamInfo  `json:"streams"`
}

// StreamCount is the per-stream packet count inside one interval.
type StreamCount struct {
	Interval        int    `json:"interval"`
	Program         uint16 `json:"program"`
	PID             uint16 `json:"pid"`
	StreamType      uint8  `json:"stream_type"`
	StreamTypeName  string `json:"stream_type_name"`
	PATVersion      int    `json:"pat_version"`
	PMTVersion      int    `json:"pmt_version"`
	EffectivePacket int    `json:"effective_packet"`
	Packets         int    `json:"packets"`
	ContinuityGaps  int    `json:"continuity_gaps"`
	MissingPackets  int    `json:"missing_packets"`
	Duplicates      int    `json:"duplicate_packets"`
	TransportErrors int    `json:"transport_errors"`
}

// UnmappedCount counts packets of a PID outside the active mapping.
type UnmappedCount struct {
	Interval int    `json:"interval"` // -1: before the first table
	PID      uint16 `json:"pid"`
	Packets  int    `json:"packets"`
}

// ContinuityEntry aggregates continuity anomalies of one PID.
type ContinuityEntry struct {
	PID             uint16 `json:"pid"`
	Gaps            int    `json:"gaps"`
	MissingPackets  int    `json:"missing_packets"`
	Duplicates      int    `json:"duplicate_packets"`
	TransportErrors int    `json:"transport_errors"`
}

// Document is the top-level JSON evidence of one run.
type Document struct {
	Input             string                 `json:"input"`
	TotalPackets      int                    `json:"total_packets"`
	PSIPackets        int                    `json:"psi_packets"`
	PATVersionChanges int                    `json:"pat_version_changes"`
	PMTVersionChanges int                    `json:"pmt_version_changes"`
	PIDMap            []IntervalMap          `json:"pid_map"`
	StreamCounts      []StreamCount          `json:"stream_packet_counts"`
	Unmapped          []UnmappedCount        `json:"unmapped_packets"`
	Continuity        []ContinuityEntry      `json:"continuity_gaps"`
	Incomplete        []psi.IncompleteUpdate `json:"incomplete_updates"`
	CorruptedSections int                    `json:"corrupted_sections"`
	Status            string                 `json:"status"`
}

// StreamTypeName names common PMT stream types.
func StreamTypeName(t uint8) string {
	switch t {
	case 0x01:
		return "MPEG-1 video"
	case 0x02:
		return "MPEG-2 video"
	case 0x03:
		return "MPEG-1 audio"
	case 0x04:
		return "MPEG-2 audio"
	case 0x0F:
		return "AAC audio"
	case 0x1B:
		return "H.264 video"
	case 0x24:
		return "H.265 video"
	default:
		return "unknown"
	}
}

// Build converts a pipeline result into the report document.
func Build(input string, res *slicer.Result) *Document {
	doc := &Document{
		Input:             input,
		TotalPackets:      res.TotalPackets,
		PSIPackets:        res.PSIPackets,
		PATVersionChanges: res.PATChanges,
		PMTVersionChanges: res.PMTChanges,
		Incomplete:        res.Incomplete,
		CorruptedSections: res.CorruptedSections,
		Status:            StatusOK,
	}
	if !res.HasTables {
		doc.Status = StatusNoTables
	}
	if doc.Incomplete == nil {
		doc.Incomplete = []psi.IncompleteUpdate{}
	}
	collectUnmapped(doc, res.Pre)
	for _, iv := range res.Intervals {
		snap := iv.Snapshot
		im := IntervalMap{
			Interval:        snap.Index,
			EffectivePacket: snap.EffectivePacket,
			PATVersion:      snap.PATVersion,
			PMTVersions:     snap.PMTVersions,
		}
		for _, s := range snap.Streams {
			im.Streams = append(im.Streams, StreamInfo{
				Program: s.Program, PID: s.PID,
				StreamType: s.StreamType, StreamTypeName: StreamTypeName(s.StreamType),
			})
			sc := StreamCount{
				Interval: snap.Index, Program: s.Program, PID: s.PID,
				StreamType: s.StreamType, StreamTypeName: StreamTypeName(s.StreamType),
				PATVersion: snap.PATVersion, PMTVersion: snap.PMTVersions[s.Program],
				EffectivePacket: snap.EffectivePacket,
				Packets:         iv.Counts[s.PID],
			}
			if a := iv.Anomalies[s.PID]; a != nil {
				sc.ContinuityGaps, sc.MissingPackets = a.Gaps, a.MissingPackets
				sc.Duplicates, sc.TransportErrors = a.Duplicates, a.TransportErrors
			}
			doc.StreamCounts = append(doc.StreamCounts, sc)
		}
		doc.PIDMap = append(doc.PIDMap, im)
		collectUnmapped(doc, iv)
	}
	if doc.PIDMap == nil {
		doc.PIDMap = []IntervalMap{}
	}
	if doc.StreamCounts == nil {
		doc.StreamCounts = []StreamCount{}
	}
	if doc.Unmapped == nil {
		doc.Unmapped = []UnmappedCount{}
	}
	pids := make([]int, 0, len(res.Continuity))
	for pid, s := range res.Continuity {
		if s.Gaps > 0 || s.Duplicates > 0 || s.TransportErrors > 0 {
			pids = append(pids, int(pid))
		}
	}
	sort.Ints(pids)
	for _, pid := range pids {
		s := res.Continuity[uint16(pid)]
		doc.Continuity = append(doc.Continuity, ContinuityEntry{
			PID: uint16(pid), Gaps: s.Gaps, MissingPackets: s.MissingPackets,
			Duplicates: s.Duplicates, TransportErrors: s.TransportErrors,
		})
	}
	if doc.Continuity == nil {
		doc.Continuity = []ContinuityEntry{}
	}
	return doc
}

func collectUnmapped(doc *Document, iv *slicer.Interval) {
	pids := make([]int, 0, len(iv.Unmapped))
	for pid := range iv.Unmapped {
		pids = append(pids, int(pid))
	}
	sort.Ints(pids)
	for _, pid := range pids {
		doc.Unmapped = append(doc.Unmapped, UnmappedCount{
			Interval: iv.Snapshot.Index, PID: uint16(pid), Packets: iv.Unmapped[uint16(pid)],
		})
	}
}

// WriteJSON writes the document as indented JSON.
func WriteJSON(w io.Writer, doc *Document) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

// WriteSummary writes a compact terminal summary.
func WriteSummary(w io.Writer, doc *Document) {
	fmt.Fprintf(w, "input: %s  packets: %d (psi: %d)  status: %s\n",
		doc.Input, doc.TotalPackets, doc.PSIPackets, doc.Status)
	fmt.Fprintf(w, "table versions: PAT +%d  PMT +%d  corrupted sections: %d\n",
		doc.PATVersionChanges, doc.PMTVersionChanges, doc.CorruptedSections)
	for _, im := range doc.PIDMap {
		fmt.Fprintf(w, "interval %d (effective at packet %d, PAT v%d, PMT %v):\n",
			im.Interval, im.EffectivePacket, im.PATVersion, im.PMTVersions)
		for _, sc := range doc.StreamCounts {
			if sc.Interval != im.Interval {
				continue
			}
			fmt.Fprintf(w, "  program %d pid %4d %-13s packets %d", sc.Program, sc.PID,
				sc.StreamTypeName, sc.Packets)
			if sc.ContinuityGaps > 0 || sc.Duplicates > 0 || sc.TransportErrors > 0 {
				fmt.Fprintf(w, "  [gaps %d missing %d dup %d te %d]",
					sc.ContinuityGaps, sc.MissingPackets, sc.Duplicates, sc.TransportErrors)
			}
			fmt.Fprintln(w)
		}
	}
	for _, u := range doc.Unmapped {
		fmt.Fprintf(w, "unmapped: interval %d pid %d packets %d\n", u.Interval, u.PID, u.Packets)
	}
	for _, c := range doc.Continuity {
		fmt.Fprintf(w, "continuity: pid %d gaps %d missing %d dup %d te %d\n",
			c.PID, c.Gaps, c.MissingPackets, c.Duplicates, c.TransportErrors)
	}
	for _, inc := range doc.Incomplete {
		fmt.Fprintf(w, "incomplete update: %s pid %d version %d sections %d/%d missing ~%d packets\n",
			inc.Table, inc.PID, inc.Version, inc.SectionsHave, inc.SectionsWant, inc.MissingPackets)
	}
}
