package index

import (
	"fmt"
	"sort"

	"mediq/internal/bitstream"
	"mediq/internal/timeline"
)

// Result is the reconciled keyframe truth for one representation.
// Proven keyframes are sorted by offset; quarantine holds index rows
// that could not be confirmed and must never reach a committed plan.
type Result struct {
	Stream     string
	Proven     []Finding // status valid or recovered, offset-ordered
	Quarantine []Finding // duplicate / out-of-range / mispoint rows
	Errors     []*timeline.Error
	IndexRows  int // entries supplied by the delivered index
	ValidHits  int // index entries proven against the bitstream
}

// HitRate returns valid entries divided by delivered rows, 1 for an
// empty index (nothing promised, nothing missed at parse time).
func (r *Result) HitRate() float64 {
	if r.IndexRows == 0 {
		return 1
	}
	return float64(r.ValidHits) / float64(r.IndexRows)
}

// ProvenEntries returns only the usable keyframe entries, in order.
func (r *Result) ProvenEntries() []Entry {
	out := make([]Entry, 0, len(r.Proven))
	for _, f := range r.Proven {
		out = append(out, f.Entry)
	}
	return out
}

// Reconcile validates an index against a scanned stream. Keyframes
// proven directly from the bitstream always win: a missing index is
// repaired, wrong pointers are quarantined, and no unconfirmed row can
// enter the result. The scan must have excluded truncated tails.
func Reconcile(f *File, scan *bitstream.ScanResult) *Result {
	r := &Result{Stream: f.Stream, IndexRows: len(f.Entries)}
	proven := scan.Keyframes() // complete, non-corrupt RAP units only
	byOffset := make(map[int64]bitstream.Unit, len(proven))
	for _, u := range proven {
		byOffset[u.Offset] = u
	}
	seenOffset := make(map[int64]bool, len(f.Entries))
	seenSeq := make(map[uint32]bool, len(f.Entries))
	validOffsets := make(map[int64]bool, len(proven))

	for _, e := range f.Entries {
		switch {
		case e.Offset >= scan.CompleteBytes || seenOffset[e.Offset] || seenSeq[e.Seq]:
			kind := timeline.KindIndexOutOfRange
			action := "quarantine_entry"
			if seenOffset[e.Offset] || seenSeq[e.Seq] {
				kind = timeline.KindIndexDuplicate
			}
			if e.Offset > scan.CompleteBytes {
				action = "quarantine_beyond_eof"
			}
			r.Errors = append(r.Errors, timeline.New(timeline.StageReconcile, kind,
				e.Offset, action, fmt.Sprintf("seq=%d pts=%d", e.Seq, int64(e.PTS))))
			r.Quarantine = append(r.Quarantine, Finding{Entry: e, Status: statusFor(kind), Source: "index"})
		default:
			u, ok := byOffset[e.Offset]
			if !ok || u.Seq != e.Seq || int64(u.PTS) != int64(e.PTS) {
				r.Errors = append(r.Errors, timeline.New(timeline.StageReconcile,
					timeline.KindIndexMispoint, e.Offset, "quarantine_entry",
					fmt.Sprintf("no keyframe unit matches seq=%d pts=%d", e.Seq, int64(e.PTS))))
				r.Quarantine = append(r.Quarantine, Finding{Entry: e, Status: StatusMispoint, Source: "index"})
			} else {
				r.Proven = append(r.Proven, Finding{Entry: e, Status: StatusValid, Source: "index"})
				r.ValidHits++
				validOffsets[e.Offset] = true
			}
		}
		seenOffset[e.Offset] = true
		seenSeq[e.Seq] = true
	}

	// Repair: every stream-proven keyframe the index failed to confirm
	// becomes a recovered, bitstream-sourced finding.
	for _, u := range proven {
		if validOffsets[u.Offset] {
			continue
		}
		e := Entry{Seq: u.Seq, Offset: u.Offset, PTS: u.PTS}
		r.Proven = append(r.Proven, Finding{Entry: e, Status: StatusRecovered, Source: "bitstream"})
		r.Errors = append(r.Errors, timeline.New(timeline.StageReconcile,
			timeline.KindIndexMissing, u.Offset, "recover_keyframe",
			fmt.Sprintf("seq=%d proven from bitstream", u.Seq)))
	}
	sort.SliceStable(r.Proven, func(i, j int) bool { return r.Proven[i].Entry.Offset < r.Proven[j].Entry.Offset })
	return r
}

func statusFor(k timeline.Kind) Status {
	switch k {
	case timeline.KindIndexDuplicate:
		return StatusDuplicate
	case timeline.KindIndexOutOfRange:
		return StatusOutOfRange
	default:
		return StatusMispoint
	}
}
