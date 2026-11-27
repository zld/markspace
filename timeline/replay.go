package timeline

import "sort"

// Replayer drives deterministic ingestion of items into a Watermark. Feeding
// the same input multiset in any arrival order yields the same final Summary,
// because every recompute re-sorts on the unified event time.
type Replayer struct {
	wm      *Watermark
	pending []Item
}

// NewReplayer wraps an existing watermark (typically NewWatermark()).
func NewReplayer(wm *Watermark) *Replayer {
	return &Replayer{wm: wm}
}

// Ingest routes one item: late items trigger an immediate rollback-recompute;
// on-time items are held until the next Advance. It returns true when the item
// was treated as late.
func (r *Replayer) Ingest(it Item) (late bool, rollbackFromNS int64) {
	if r.wm.Late(it.NS) {
		from, _ := r.wm.Replay(it)
		r.wm.Snapshot()
		return true, from
	}
	r.pending = append(r.pending, it)
	return false, 0
}

// Advance commits every pending item at or below ns and takes a checkpoint.
func (r *Replayer) Advance(ns int64) []Item {
	committed := r.wm.Advance(ns, r.pending)
	kept := r.pending[:0]
	for _, it := range r.pending {
		if it.NS > ns {
			kept = append(kept, it)
		}
	}
	r.pending = kept
	r.wm.Snapshot()
	return committed
}

// Watermark exposes the underlying watermark.
func (r *Replayer) Watermark() *Watermark { return r.wm }

// ReplaySorted is the reference path for verification: sort every item by
// event time and advance one monotonic watermark over the whole set. Its
// Summary is the canonical result that late-arrival runs must converge to.
func ReplaySorted(items []Item, gaps []Gap) Summary {
	sorted := append([]Item(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].NS < sorted[j].NS })
	wm := NewWatermark()
	var maxNS int64
	for _, it := range sorted {
		if it.NS > maxNS {
			maxNS = it.NS
		}
	}
	wm.Advance(maxNS, sorted)
	return wm.Summary(gaps)
}
