// Package manifest fans the per-layer packet streams back in over the shared
// integer clock, aligns them to common segment boundaries, tolerates late
// keyframes within a configurable wait budget, and emits a deterministic
// manifest plus per-representation quality and bitrate statistics.
package manifest

import (
	"fmt"
	"sort"

	"mediad/internal/clock"
)

// RepInfo describes one representation in the manifest.
type RepInfo struct {
	Index         int
	Name          string
	TargetBPS     int
	ActualBPS     int
	BitrateErrPct float64 // absolute percentage difference, rounded to 0.01
	Quality       float64 // delivered/expected packet ratio in [0,1]
	Dropped       int64
	Lost          int64
	Recovered     int
	// Isolated marks a representation that survived a single-layer fault and
	// was recovered; it stays in the usable list but is exempt from the strict
	// healthy-layer thresholds during verification.
	Isolated    bool
	MissingSegs []int
}

// SegmentReport is one aligned segment across every representation.
type SegmentReport struct {
	Index        int
	StartTick    int64
	EndTick      int64
	Present      []bool
	MissingLayer []int
}

// Manifest is the deterministic output of the shared-clock fan-in.
type Manifest struct {
	Timescale       uint32
	SegmentTicks    int64
	Segments        []SegmentReport
	Representations []RepInfo
	// AlignmentErrorMS is the worst cross-layer boundary error observed.
	AlignmentErrorMS int64
	// Summary is a stable textual digest input; callers hash/print it.
	Summary string
}

// LayerInput is one layer's ordered packets already mapped to the master
// clock, plus the counters produced by the stream stage.
type LayerInput struct {
	Index     int
	Name      string
	TargetBPS int
	Packets   []ClockedPacket
	Dropped   int64
	Lost      int64
	Recovered int
	// Isolated marks a layer that was damaged and recovered independently.
	Isolated bool
	// LateKeys are packet indexes (into Packets) whose keyframe arrives after
	// the configured wait budget, forcing a missing-segment mark.
	LateKeys map[int]bool
}

// ClockedPacket is the manifest-stage view of a stream Envelope.
type ClockedPacket struct {
	Seq   uint16
	TS    int64
	Key   bool
	Bytes int
}

// NewPacket builds a master-clock packet for a layer input.
func NewPacket(seq uint16, ts int64, key bool, size int) ClockedPacket {
	return ClockedPacket{Seq: seq, TS: ts, Key: key, Bytes: size}
}

// Builder holds fan-in parameters.
type Builder struct {
	timescale    uint32
	segmentTicks int64
	waitTicks    int64
	// total duration in master ticks, for bitrate/quality normalisation.
	durationTicks int64
}

// NewBuilder validates and constructs a fan-in builder.
func NewBuilder(timescale uint32, segmentTicks, waitTicks, durationTicks int64) (*Builder, error) {
	if timescale == 0 || segmentTicks <= 0 || durationTicks <= 0 {
		return nil, clock.NewError(clock.StageManifest,
			"timescale, segment ticks and duration must be positive", false)
	}
	if waitTicks < 0 || waitTicks > segmentTicks {
		return nil, clock.NewError(clock.StageManifest,
			"wait budget must be within one segment", false)
	}
	return &Builder{
		timescale: timescale, segmentTicks: segmentTicks,
		waitTicks: waitTicks, durationTicks: durationTicks,
	}, nil
}

// Build fans every layer in over the shared boundaries and produces the
// manifest. A layer whose segment-start keyframe is later than the wait
// budget is marked missing for that segment only; other layers advance.
func (b *Builder) Build(inputs []LayerInput) (*Manifest, error) {
	if len(inputs) == 0 {
		return nil, clock.NewError(clock.StageManifest, "no layer inputs", false)
	}
	nSeg := int((b.durationTicks + b.segmentTicks - 1) / b.segmentTicks)
	m := &Manifest{
		Timescale: b.timescale, SegmentTicks: b.segmentTicks,
		Segments: make([]SegmentReport, 0, nSeg),
	}
	cursors := make([]int, len(inputs)) // per-layer packet scan cursor
	presentAll := make([][]bool, len(inputs))
	for i := range presentAll {
		presentAll[i] = make([]bool, nSeg)
	}

	for seg := 0; seg < nSeg; seg++ {
		start := int64(seg) * b.segmentTicks
		end := start + b.segmentTicks
		if end > b.durationTicks {
			end = b.durationTicks
		}
		report := SegmentReport{Index: seg, StartTick: start, EndTick: end,
			Present: make([]bool, len(inputs))}
		for li, in := range inputs {
			ok, next := b.layerHasSegment(in, cursors[li], start, end)
			cursors[li] = next
			report.Present[li] = ok
			if !ok {
				report.MissingLayer = append(report.MissingLayer, li)
			} else {
				presentAll[li][seg] = true
			}
		}
		m.Segments = append(m.Segments, report)
	}

	for li, in := range inputs {
		m.Representations = append(m.Representations, b.repStats(in, presentAll[li], nSeg))
	}
	m.AlignmentErrorMS = b.worstAlignment(inputs)
	m.Summary = b.summary(m)
	return m, nil
}

// layerHasSegment scans a layer's packets for a usable keyframe at the
// segment start inside the wait budget and at least one covering packet.
func (b *Builder) layerHasSegment(in LayerInput, cursor int, start, end int64) (bool, int) {
	idx := cursor
	keyInBudget := false
	covers := false
	for idx < len(in.Packets) && in.Packets[idx].TS < end {
		p := in.Packets[idx]
		if p.Key && p.TS >= start && p.TS <= start+b.waitTicks && !in.LateKeys[idx] {
			keyInBudget = true
		}
		if p.TS >= start {
			covers = true
		}
		idx++
	}
	// Advance the cursor past packets fully before this segment start.
	for cursor < len(in.Packets) && in.Packets[cursor].TS < start {
		cursor++
	}
	return keyInBudget && covers, idx
}

// repStats computes deterministic bitrate and quality for one representation.
func (b *Builder) repStats(in LayerInput, present []bool, nSeg int) RepInfo {
	rep := RepInfo{Index: in.Index, Name: in.Name, TargetBPS: in.TargetBPS,
		Dropped: in.Dropped, Lost: in.Lost, Recovered: in.Recovered, Isolated: in.Isolated}
	var deliveredBytes int64
	var delivered int64
	for _, p := range in.Packets {
		if p.TS < b.durationTicks {
			deliveredBytes += int64(p.Bytes)
			delivered++
		}
	}
	// Expected packet count includes observed loss so quality reflects the
	// full offered workload, not just what survived.
	expected := delivered + in.Lost + in.Dropped
	if expected > 0 {
		rep.Quality = float64(delivered) / float64(expected)
	} else {
		rep.Quality = 1
	}
	if rep.Quality > 1 {
		rep.Quality = 1
	}
	durationSec := float64(b.durationTicks) / float64(b.timescale)
	if durationSec > 0 {
		rep.ActualBPS = int(float64(deliveredBytes*8) / durationSec)
	}
	if in.TargetBPS > 0 {
		diff := float64(rep.ActualBPS-in.TargetBPS) / float64(in.TargetBPS) * 100
		if diff < 0 {
			diff = -diff
		}
		rep.BitrateErrPct = float64(int(diff*100+0.5)) / 100
	}
	for seg, ok := range present {
		if !ok {
			rep.MissingSegs = append(rep.MissingSegs, seg)
		}
	}
	return rep
}

// worstAlignment measures, for each shared segment boundary, the integer-ms
// offset of the layer's first keyframe at or after that boundary (and within
// the wait budget). Interior keyframes are not segment anchors and are
// excluded so the metric reflects real boundary alignment.
func (b *Builder) worstAlignment(inputs []LayerInput) int64 {
	nSeg := int((b.durationTicks + b.segmentTicks - 1) / b.segmentTicks)
	var worst int64
	for seg := 0; seg < nSeg; seg++ {
		start := int64(seg) * b.segmentTicks
		for _, in := range inputs {
			for pi, p := range in.Packets {
				if !p.Key || p.TS < start || in.LateKeys[pi] {
					continue
				}
				if p.TS > start+b.waitTicks {
					break
				}
				errTicks := p.TS - start
				if errTicks < 0 {
					errTicks = -errTicks
				}
				errMS := errTicks * 1000 / int64(b.timescale)
				if errMS > worst {
					worst = errMS
				}
				break
			}
		}
	}
	return worst
}

// summary renders a stable digest string: sorted numeric fields only, so the
// same inputs always produce the same summary across runs.
func (b *Builder) summary(m *Manifest) string {
	segs := len(m.Segments)
	parts := make([]string, 0, len(m.Representations))
	reps := append([]RepInfo(nil), m.Representations...)
	sort.Slice(reps, func(i, j int) bool { return reps[i].Index < reps[j].Index })
	for _, r := range reps {
		parts = append(parts, fmt.Sprintf(
			"rep%d:%s:tbps=%d:abps=%d:q=%.4f:drop=%d:lost=%d:miss=%v",
			r.Index, r.Name, r.TargetBPS, r.ActualBPS, r.Quality,
			r.Dropped, r.Lost, r.MissingSegs))
	}
	return fmt.Sprintf("ts=%d:segTicks=%d:segments=%d:alignMS=%d|%s",
		b.timescale, b.segmentTicks, segs, m.AlignmentErrorMS,
		joinStrings(parts, ";"))
}

func joinStrings(ss []string, sep string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}
