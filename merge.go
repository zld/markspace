package mediacore

import "sort"

// Conflict captures two or more versions covering the same slot with
// non-identical payloads; both (all) sources are retained for inspection.
type Conflict struct {
	Key      Key
	Range    CoveredRange
	Versions []SourceMeta
	Fingers  [][32]byte
}

// MergeStats summarizes a merge pass for the budget report.
type MergeStats struct {
	Candidates int
	Processed  int
	Rejected   int
	Conflicts  int
	Idempotent int
}

// Budget bounds processing effort and in-memory working set. The demo measures
// actual elapsed time against TimeBudgetMS; MemBudgetBytes caps buffered bytes.
type Budget struct {
	TimeBudgetMS    int64
	MemBudgetBytes  int64
	ActualElapsedMS int64
	MemBytes        int64
	Exceeded        bool
}

// Merge reconciles grouped items slot by slot. Byte-identical re-coverage is
// idempotent and collapses to one committed item; differing payloads are
// reported as conflicts and every source is kept but left uncommitted.
func Merge(groups map[Key][]*Item, budget *Budget) ([]*Item, []Conflict, []*Error, int) {
	keys := make([]Key, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].TrackID != keys[j].TrackID {
			return keys[i].TrackID < keys[j].TrackID
		}
		return keys[i].Seq < keys[j].Seq
	})
	var committed []*Item
	var conflicts []Conflict
	var problems []*Error
	idempotent := 0
	for _, k := range keys {
		group := groups[k]
		var live []*Item
		for _, it := range group {
			if it.Status == "rejected" {
				problems = append(problems, it.Reason)
				continue
			}
			live = append(live, it)
		}
		if len(live) == 0 {
			continue
		}
		// Memory accounting happens before any slot is committed.
		for _, it := range live {
			budget.MemBytes += int64(len(it.Payload))
		}
		if budget.MemBudgetBytes > 0 && budget.MemBytes > budget.MemBudgetBytes {
			budget.Exceeded = true
			for _, it := range live {
				reject(it, WrapMsg(ErrMemoryExceeded, it.Asset, it.TrackID, StageMerge,
					it.Ref.ByteOff, "working set over memory budget"))
				problems = append(problems, it.Reason)
			}
			continue
		}
		identical := true
		for i := 1; i < len(live); i++ {
			if live[i].Finger != live[0].Finger {
				identical = false
				break
			}
		}
		if identical {
			primary := live[0]
			primary.Status = "processed"
			if len(live) > 1 {
				idempotent += len(live) - 1
				// Extra identical copies are absorbed, not committed twice, and
				// are counted separately from the canonical processed item.
				for _, dup := range live[1:] {
					dup.Status = "absorbed"
					dup.Reason = nil
				}
			}
			committed = append(committed, primary)
			continue
		} // Conflicting coverage: retain every source, commit nothing.
		cf := Conflict{Key: k, Range: live[0].Range}
		for _, it := range live {
			cf.Versions = append(cf.Versions, SourceMeta{Asset: it.Asset, Version: it.Version, Track: it.TrackID})
			cf.Fingers = append(cf.Fingers, it.Finger)
			reject(it, WrapMsg(ErrConflict, it.Asset, it.TrackID, StageMerge, it.Ref.ByteOff,
				"conflicting payloads for track "+utoa(it.TrackID)+" seq "+utoa(it.Seq)))
			problems = append(problems, it.Reason)
		}
		conflicts = append(conflicts, cf)
	}
	return committed, conflicts, problems, idempotent
}

func reject(it *Item, e *Error) {
	it.Status = "rejected"
	it.Reason = e
}

// Summarize derives the fixed-field counters printed by the report.
func Summarize(committed []*Item, all []*Item, conflicts []Conflict, idempotent int) MergeStats {
	s := MergeStats{Candidates: len(all), Conflicts: len(conflicts), Idempotent: idempotent}
	seen := map[Key]bool{}
	for _, it := range committed {
		k := Key{it.TrackID, it.Seq}
		if !seen[k] {
			seen[k] = true
			s.Processed++
		}
	}
	for _, it := range all {
		if it.Status == "rejected" {
			s.Rejected++
		}
	}
	return s
}
