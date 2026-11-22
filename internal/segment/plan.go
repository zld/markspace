// Package segment turns reconciled keyframe timelines into segment
// plans and aligns the plans of every representation of one asset so
// that boundary numbers and target times are shared across bitrates.
package segment

import (
	"fmt"
	"sort"

	"mediq/internal/bitstream"
	"mediq/internal/index"
	"mediq/internal/timeline"
)

// Source records where a segment boundary came from.
type Source string

const (
	SourceIndex     Source = "index"
	SourceBitstream Source = "bitstream"
	SourceAligned   Source = "aligned"
)

// Segment is one planned, not yet materialized media segment.
type Segment struct {
	Index       int // shared boundary number across representations
	RepID       string
	Codec       bitstream.Codec
	StartPTS    timeline.Ticks // PTS of the first unit in the segment
	EndPTS      timeline.Ticks // PTS just past the last unit
	StartOffset int64          // byte offset of the first unit
	EndOffset   int64          // byte offset just past the last unit
	Bytes       int64
	KeyframeSeq uint32
	IndexSource Source
	Quarantined bool // contains a corrupt unit; routed to quarantine output
}

// Plan is the segment plan for one representation.
type Plan struct {
	RepID      string
	Codec      bitstream.Codec
	Segments   []*Segment
	Keyframes  []index.Entry
	Quarantine int // corrupt units observed while planning
}

// Config controls plan generation.
type Config struct {
	TargetDuration timeline.Ticks // nominal segment duration on the unified clock
}

// DefaultConfig returns a 2 s target duration.
func DefaultConfig() Config { return Config{TargetDuration: timeline.MustMs(2000)} }

// span is one keyframe-to-next-keyframe range before merging.
type span struct {
	kf          index.Entry
	src         Source
	start, end  int64
	startPTS    timeline.Ticks
	endPTS      timeline.Ticks
	corrupt     bool
	corruptHits int
}

// Build creates a segment plan. Boundaries always start on proven
// keyframes; spans are merged until the next keyframe is at or beyond
// TargetDuration from the current boundary. A span containing a
// corrupt unit is never merged with clean neighbours: it becomes its
// own quarantined segment so unprovable bytes cannot reach clean output.
func Build(repID string, scan *bitstream.ScanResult, rec *index.Result, cfg Config) (*Plan, error) {
	if cfg.TargetDuration <= 0 {
		return nil, fmt.Errorf("segment: target duration must be positive, got %d", cfg.TargetDuration)
	}
	plan := &Plan{RepID: repID, Codec: scan.Codec, Keyframes: rec.ProvenEntries()}
	if len(plan.Keyframes) == 0 {
		return nil, timeline.New(timeline.StagePlan, timeline.KindInconsistent, -1,
			"abort_plan", "no provable keyframes: cannot build any segment")
	}
	sourceByOffset := make(map[int64]Source, len(rec.Proven))
	for _, f := range rec.Proven {
		if f.Status == index.StatusValid {
			sourceByOffset[f.Entry.Offset] = SourceIndex
		} else {
			sourceByOffset[f.Entry.Offset] = SourceBitstream
		}
	}

	// Pass 1: split on EVERY random access unit, including corrupt ones,
	// so a damaged key frame forms its own quarantined span rather than
	// disappearing from the plan. Provenance for clean spans comes from
	// the reconciled index; corrupt spans carry bitstream provenance.
	raps := scan.RAPUnits()
	spans := make([]span, 0, len(raps))
	for ki, rap := range raps {
		limit := scan.CompleteBytes
		if ki+1 < len(raps) {
			limit = raps[ki+1].Offset
		}
		sp := span{
			kf:       index.Entry{Seq: rap.Seq, Offset: rap.Offset, PTS: rap.PTS},
			start:    rap.Offset,
			end:      limit,
			startPTS: rap.PTS,
			src:      sourceFor(rap.Offset, sourceByOffset),
		}
		var lastPTS timeline.Ticks
		for _, u := range scan.Units {
			if u.Offset < rap.Offset || u.Offset >= limit {
				continue
			}
			lastPTS = u.PTS
			if u.Corrupt {
				sp.corrupt = true
				sp.corruptHits++
			}
		}
		sp.endPTS = lastPTS
		spans = append(spans, sp)
		plan.Quarantine += sp.corruptHits
	}

	// Pass 2: merge clean spans up to the target duration; keep each
	// corrupt span as an isolated quarantined segment. The accumulator
	// `acc` is the pending segment; it is emitted with the current
	// boundary number whenever duration, corruption or end-of-stream
	// forces a boundary.
	var acc *span
	boundary := 0
	emit := func(sp span) {
		plan.Segments = append(plan.Segments, &Segment{
			Index:       boundary,
			RepID:       repID,
			Codec:       scan.Codec,
			StartPTS:    sp.startPTS,
			EndPTS:      sp.endPTS,
			StartOffset: sp.start,
			EndOffset:   sp.end,
			Bytes:       sp.end - sp.start,
			KeyframeSeq: sp.kf.Seq,
			IndexSource: sp.src,
			Quarantined: sp.corrupt,
		})
		boundary++
	}
	for i := range spans {
		cur := &spans[i]
		switch {
		case cur.corrupt:
			// Damage never merges: flush any clean accumulator first,
			// then emit the corrupt span on its own boundary.
			if acc != nil {
				emit(*acc)
				acc = nil
			}
			emit(*cur)
		case acc == nil:
			acc = cur // start a new clean accumulator
		case cur.startPTS-acc.startPTS < cfg.TargetDuration:
			acc.end, acc.endPTS = cur.end, cur.endPTS // absorb span
		default:
			emit(*acc) // target duration reached
			acc = cur
		}
	}
	if acc != nil {
		emit(*acc)
	}
	return plan, nil
}

func sourceFor(off int64, m map[int64]Source) Source {
	if s, ok := m[off]; ok {
		return s
	}
	return SourceBitstream
}

// SortedPlans orders plans by representation ID for stable output.
func SortedPlans(plans []*Plan) {
	sort.Slice(plans, func(i, j int) bool { return plans[i].RepID < plans[j].RepID })
}
