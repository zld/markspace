package mediacore

import (
	"errors"
)

// RunResult is everything the entry layer needs to render a report.
type RunResult struct {
	Report    *Report
	Journal   *Journal
	ParseFail []*Error
	GraphFail []*Error
	MetaFail  []*Error
}

// RunConfig parameterizes one deterministic pipeline pass.
type RunConfig struct {
	BudgetMS       int64
	MemBudgetBytes int64
	PerItemCostMS  int64 // deterministic per-candidate charge for the budget model
	JournalPath    string
}

// Run executes the full kernel pipeline against an in-memory scenario. It is
// pure apart from journal persistence at JournalPath; no network or device is
// touched. Rejected inputs never reach the commit gate, and a post-merge
// anomaly aborts its batch through the recovery journal.
func Run(sc *Scenario, cfg RunConfig) (*RunResult, error) {
	loader := NewMemLoader(sc)
	budget := &Budget{TimeBudgetMS: cfg.BudgetMS, MemBudgetBytes: cfg.MemBudgetBytes}

	var playlists []*Playlist
	var parseFail []*Error
	var graphFail []*Error
	graphReject := map[uint32]bool{}

	// Stage 1+2: parse manifests and validate the track dependency graph.
	for _, name := range sortedKeys(sc.Manifests) {
		pl, err := ParsePlaylist(name, stringReader(sc.Manifests[name]))
		if err != nil {
			parseFail = append(parseFail, asCtx(err))
			continue
		}
		if _, gerr := ValidateGraph(pl.AssetID, pl.Tracks); gerr != nil {
			ge := asCtx(gerr)
			graphFail = append(graphFail, ge)
			for _, id := range cyclicTracks(ge) {
				graphReject[id] = true
			}
		} else {
			playlists = append(playlists, pl)
		}
	}

	// Stage 3: map versions to (track, seq) slots, loading local fragments.
	items, _ := Map(playlists, loader)

	// Stage 4: metadata arbitration (audio layout/rate, color priority).
	metaFail, colorRes := applyMetadata(playlists, items)
	for _, it := range items {
		if it.Status == "rejected" {
			continue
		}
		for _, f := range metaFail {
			if f.Track == it.TrackID && f.Asset == it.Asset {
				reject(it, WrapMsg(ErrConflict, it.Asset, it.TrackID, StageMetadata,
					it.Ref.ByteOff, "audio metadata could not be reconciled"))
				break
			}
		}
	}

	// Stage 5: graph-rejected slots never enter the merge.
	var live []*Item
	for _, it := range items {
		if graphReject[it.TrackID] {
			reject(it, WrapMsg(ErrCycle, it.Asset, it.TrackID, StageGraph,
				it.Ref.ByteOff, "track belongs to a cyclic dependency set"))
			continue
		}
		if it.Status != "rejected" {
			live = append(live, it)
		}
	}

	// Stage 6: deterministic budget accounting and idempotent/conflict merge.
	chargeBudget(live, cfg.PerItemCostMS, budget)
	groups := GroupByKey(live)
	committed, conflicts, _, _ := Merge(groups, budget)

	// Stage 7: anomaly pre-check. An RTP gap aborts the owning slot and is
	// rolled back as a batch, so it is never published.
	journal, err := LoadJournal(cfg.JournalPath)
	if err != nil {
		return nil, err
	}
	var rollbacks []RollbackRecord
	anomalies, abortKeys := precheckAnomalies(sc, loader, committed)
	if len(abortKeys) > 0 {
		abortSet := map[Key]bool{}
		for _, k := range abortKeys {
			abortSet[k] = true
		}
		var keep []*Item
		for _, it := range committed {
			if abortSet[Key{it.TrackID, it.Seq}] {
				reject(it, WrapMsg(ErrAnomaly, it.Asset, it.TrackID, StageSegment,
					it.Ref.ByteOff, "rtp sequence anomaly; batch aborted"))
				continue
			}
			keep = append(keep, it)
		}
		committed = keep
		recs := journal.AbortBatch(abortKeys, StageSegment, int64(anomalies[0].StartNS),
			"rtp sequence anomaly; tentative commit rolled back", anomalies[0].StartNS)
		rollbacks = append(rollbacks, recs...)
	}

	// Stage 8: all-or-nothing commit of everything that survived.
	if _, perr := journal.PublishBatch(keysOf(committed), func() error { return nil }); perr != nil {
		return nil, perr
	}
	if err := journal.Save(cfg.JournalPath); err != nil {
		return nil, err
	}
	rollbacks = append(rollbacks, journal.Rollbacks()...)

	// Stage 9: assemble the deterministic report.
	allItems := appendSyntheticRejects(items, parseFail, graphFail)
	report := BuildReport(ReportInput{
		Assets:    sc.Order,
		Items:     allItems,
		Missing:   MissingReport(allItems),
		Conflicts: conflicts,
		Rollbacks: dedupRollbacks(rollbacks),
		Budget:    budget,
		Anomalies: anomalies,
		Committed: journal.Committed(),
		ColorRes:  colorRes,
	})
	return &RunResult{Report: report, Journal: journal,
		ParseFail: parseFail, GraphFail: graphFail, MetaFail: metaFail}, nil
}

// appendSyntheticRejects ensures parse- and graph-level failures appear as
// itemized rejections even though they produced no mapped Item.
func appendSyntheticRejects(items []*Item, parseFail, graphFail []*Error) []*Item {
	out := append([]*Item{}, items...)
	for _, e := range parseFail {
		out = append(out, syntheticItem(e))
	}
	for _, e := range graphFail {
		if len(e.Tracks) > 0 {
			for _, id := range e.Tracks {
				cp := *e
				cp.Track = id
				out = append(out, syntheticItem(&cp))
			}
		} else {
			out = append(out, syntheticItem(e))
		}
	}
	return out
}

func cyclicTracks(e *Error) []uint32 {
	if len(e.Tracks) > 0 {
		return e.Tracks
	}
	return []uint32{e.Track}
}

func syntheticItem(e *Error) *Item {
	return &Item{Asset: e.Asset, Version: "v1", TrackID: e.Track,
		Ref: &SegmentRef{ByteOff: e.Offset}, Status: "rejected", Reason: e}
}

func asCtx(err error) *Error {
	var me *Error
	if errors.As(err, &me) {
		return me
	}
	return Wrap(err, "", 0, StagePlaylist, -1)
}
