// Package segment forms variable-frame-rate intervals from actual
// sample times. Boundaries come from observed sample counts only;
// missing samples are recorded as gaps, never fabricated, and every
// boundary sample belongs to exactly one segment.
package segment

import (
	"fmt"

	"rtx/internal/model"
)

// MinPackets is the default minimum segment length; shorter runs merge
// into a neighbor and record their source range.
const MinPackets = 3

// Segment is one half-open packet interval [StartPacket, EndPacket)
// covering the half-open sample interval [StartSample, EndSample).
type Segment struct {
	Index       int
	StartPacket int
	EndPacket   int
	StartSample uint64
	EndSample   uint64
	FrameSamples uint64 // dominant inter-packet sample delta
	Merged      bool
	Sources     []model.Range // packet ranges merged into this segment
	Gaps        []model.Range // missing sample ranges inside the segment
}

// Packets returns the number of packets in the segment.
func (s Segment) Packets() int { return s.EndPacket - s.StartPacket }

// Build segments the ordered per-packet sample counts. samples[i] is
// the cumulative sample index of packet i on the monotonic sample
// timeline. Segments never overlap: packet p and sample boundary each
// belong to exactly one segment.
func Build(samples []uint64, minPackets int) []Segment {
	n := len(samples)
	if n == 0 {
		return nil
	}
	if minPackets <= 0 {
		minPackets = MinPackets
	}
	if n == 1 {
		return []Segment{{Index: 0, StartPacket: 0, EndPacket: 1,
			StartSample: samples[0], EndSample: samples[0]}}
	}
	type run struct {
		start, end int // half-open packet range
		frame      uint64
		sources    []model.Range
	}
	// Run-length encode equal inter-packet deltas. Each packet is
	// grouped with its outgoing delta, so runs never share a packet;
	// the final packet joins the last run.
	var runs []run
	start := 0
	frame := samples[1] - samples[0]
	for i := 1; i+1 < n; i++ {
		if d := samples[i+1] - samples[i]; d != frame {
			runs = append(runs, run{start: start, end: i, frame: frame})
			start, frame = i, d
		}
	}
	runs = append(runs, run{start: start, end: n, frame: frame})
	// Merge short runs into the longer neighbor until none remain.
	for {
		k := -1
		for i, r := range runs {
			if r.end-r.start < minPackets && len(runs) > 1 {
				k = i
				break
			}
		}
		if k < 0 {
			break
		}
		target := k - 1
		if k == 0 || (k+1 < len(runs) && runs[k+1].end-runs[k+1].start > runs[k-1].end-runs[k-1].start) {
			target = k + 1
		}
		if target < 0 {
			target = 1
		}
		if target > k { // merge forward
			runs[target].start = runs[k].start
			runs[target].sources = append(runs[target].sources,
				model.Range{Start: int64(runs[k].start), End: int64(runs[k].end)})
			runs[target].sources = append(runs[target].sources, runs[k].sources...)
		} else { // merge backward
			runs[target].end = runs[k].end
			runs[target].sources = append(runs[target].sources,
				model.Range{Start: int64(runs[k].start), End: int64(runs[k].end)})
			runs[target].sources = append(runs[target].sources, runs[k].sources...)
		}
		runs = append(runs[:k], runs[k+1:]...)
	}
	// Materialize segments with half-open sample ranges and gap records.
	out := make([]Segment, len(runs))
	for i, r := range runs {
		end := samples[r.end-1]
		if i+1 < len(runs) {
			end = samples[r.end]
		} else if r.end-r.start > 1 {
			end = samples[r.end-1] + r.frame
		}
		out[i] = Segment{
			Index: i, StartPacket: r.start, EndPacket: r.end,
			StartSample: samples[r.start], EndSample: end,
			FrameSamples: r.frame, Merged: len(r.sources) > 0, Sources: r.sources,
		}
		// Interior deltas that are multiples of the dominant frame are
		// missing packets: record the hole, fabricate nothing.
		for p := r.start; p+1 < r.end; p++ {
			d := samples[p+1] - samples[p]
			if r.frame > 0 && d > r.frame && d%r.frame == 0 {
				out[i].Gaps = append(out[i].Gaps,
					model.Range{Start: int64(samples[p]), End: int64(samples[p+1])})
			}
		}
	}
	return out
}

// Coverage returns the share of the total sample span covered by
// segments, excluding gap ranges, in [0,1].
func Coverage(segs []Segment) float64 {
	if len(segs) == 0 {
		return 0
	}
	total := segs[len(segs)-1].EndSample - segs[0].StartSample
	if total == 0 {
		return 0
	}
	var gaps uint64
	for _, s := range segs {
		for _, g := range s.Gaps {
			gaps += uint64(g.End - g.Start)
		}
	}
	if gaps >= total {
		return 0
	}
	return float64(total-gaps) / float64(total)
}

// Describe renders a one-line summary for terminal diagnostics.
func (s Segment) Describe() string {
	return fmt.Sprintf("segment %d: packets [%d,%d) samples [%d,%d) frame=%d merged=%v",
		s.Index, s.StartPacket, s.EndPacket, s.StartSample, s.EndSample, s.FrameSamples, s.Merged)
}
