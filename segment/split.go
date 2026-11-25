// Package segment turns fan-in-checked boundaries into stable segments with
// explicit start/end times and source packet ranges. Packets belonging to
// isolated anomaly regions are never silently dropped: the segments touching
// them carry a Damaged marker with the anomaly reasons.
package segment

import (
	"sort"

	"rtpreplay/ingest"
	"rtpreplay/media"
	"rtpreplay/pipeline"
)

// Segment is one stable cut of a representation.
type Segment struct {
	Index                  int
	BitrateKbps            int
	Kind                   string
	StartMS, EndMS         float64
	StartRecord, EndRecord int
	StartExtSeq, EndExtSeq uint64
	Damaged                bool
	Reasons                []string
}

// Split builds segments from the representation's boundaries. Consecutive
// boundaries delimit spans; the final span runs to the last observed packet.
func Split(rep *pipeline.Representation) []Segment {
	bounds := rep.Boundaries
	pkts := rep.Stream.Packets
	if len(bounds) == 0 || len(pkts) == 0 {
		return nil
	}
	rate := rep.Stream.ClockRate
	baseMS := media.TicksToMS(pkts[0].Clock.Unwrapped, rate)

	var segs []Segment
	for i, b := range bounds {
		startRecord := b.FirstRecord
		var endRecord int
		if i+1 < len(bounds) {
			endRecord = bounds[i+1].FirstRecord - 1
		} else {
			endRecord = pkts[len(pkts)-1].RecordIndex
		}
		if endRecord < startRecord {
			continue // snapped boundaries that collapsed onto one sample
		}
		endPkt := packetAtRecord(pkts, endRecord)
		if endPkt == nil {
			continue
		}
		seg := Segment{
			Index:       i,
			BitrateKbps: rep.BitrateKbps,
			Kind:        rep.Kind,
			StartMS:     b.TimeMS,
			EndMS:       media.TicksToMS(endPkt.Clock.Unwrapped, rate) - baseMS,
			StartRecord: startRecord,
			EndRecord:   endRecord,
			StartExtSeq: b.ExtSeqStart,
			EndExtSeq:   endPkt.Clock.ExtSeq,
		}
		seg.Damaged, seg.Reasons = damageWithin(pkts, startRecord, endRecord)
		segs = append(segs, seg)
	}
	return segs
}

// packetAtRecord binary-searches packets, which are stored in record order.
func packetAtRecord(pkts []*ingest.Packet, record int) *ingest.Packet {
	idx := sort.Search(len(pkts), func(i int) bool {
		return pkts[i].RecordIndex >= record
	})
	if idx < len(pkts) && pkts[idx].RecordIndex == record {
		return pkts[idx]
	}
	return nil
}

func damageWithin(pkts []*ingest.Packet, startRecord, endRecord int) (bool, []string) {
	seen := map[string]bool{}
	var reasons []string
	damaged := false
	for _, p := range pkts {
		if p.RecordIndex < startRecord || p.RecordIndex > endRecord {
			continue
		}
		if p.Clock.Isolated {
			damaged = true
			if reason := string(p.Clock.Anomaly); reason != "" && !seen[reason] {
				seen[reason] = true
				reasons = append(reasons, reason)
			}
		}
	}
	return damaged, reasons
}
