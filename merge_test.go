package mediacore

import (
	"errors"
	"testing"
)

func mkItem(asset, version string, track, seq uint32, payload string, status string) *Item {
	it := &Item{Asset: asset, Version: version, TrackID: track, Seq: seq,
		Payload: []byte(payload), Status: status,
		Ref: &SegmentRef{ByteOff: 128}}
	it.Finger = FingerprintPayload(it.Payload)
	return it
}

func TestMergeIdempotentIdenticalCoverage(t *testing.T) {
	groups := map[Key][]*Item{
		{1, 1}: {
			mkItem("a", "v1", 1, 1, "same-bytes", "pending"),
			mkItem("a", "v2", 1, 1, "same-bytes", "pending"),
		},
	}
	budget := &Budget{MemBudgetBytes: 1 << 20}
	committed, conflicts, problems, idem := Merge(groups, budget)
	if len(committed) != 1 || len(conflicts) != 0 || len(problems) != 0 {
		t.Fatalf("want one clean commit, got committed=%d conflicts=%d problems=%d",
			len(committed), len(conflicts), len(problems))
	}
	if idem != 1 {
		t.Fatalf("idempotent count = %d want 1", idem)
	}
	if committed[0].Status != "processed" {
		t.Fatalf("primary not processed: %s", committed[0].Status)
	}
	// Re-running on the same grouping stays stable.
	committed2, conflicts2, _, _ := Merge(groups, budget)
	if len(committed2) != len(committed) || len(conflicts2) != 0 {
		t.Fatal("repeat merge must be deterministic")
	}
}

func TestMergeConflictRetainsBothSources(t *testing.T) {
	groups := map[Key][]*Item{
		{2, 3}: {
			mkItem("a", "v1", 2, 3, "payload-alpha", "pending"),
			mkItem("a", "v2", 2, 3, "payload-beta", "pending"),
		},
	}
	budget := &Budget{MemBudgetBytes: 1 << 20}
	committed, conflicts, problems, _ := Merge(groups, budget)
	if len(committed) != 0 {
		t.Fatal("conflicting slot must not commit")
	}
	if len(conflicts) != 1 || len(conflicts[0].Versions) != 2 {
		t.Fatalf("conflict must retain both sources: %+v", conflicts)
	}
	if len(problems) != 2 {
		t.Fatalf("both sources rejected individually, got %d", len(problems))
	}
	for _, p := range problems {
		if !errors.Is(p, ErrConflict) || p.Stage != StageMerge || p.Offset != 128 {
			t.Fatalf("rejection missing stage/offset context: %+v", p)
		}
	}
}

func TestMergeMemoryBudgetRejectsAndLeavesNothingCommitted(t *testing.T) {
	groups := map[Key][]*Item{
		{1, 1}: {mkItem("a", "v1", 1, 1, "abcdefghij", "pending")},
	}
	budget := &Budget{MemBudgetBytes: 4}
	committed, _, problems, _ := Merge(groups, budget)
	if len(committed) != 0 {
		t.Fatal("over-budget slot must never commit")
	}
	if !budget.Exceeded || len(problems) != 1 || !errors.Is(problems[0], ErrMemoryExceeded) {
		t.Fatalf("budget rejection not recorded: exceeded=%t problems=%d", budget.Exceeded, len(problems))
	}
}

func TestResolveAudioConversionAndUnknownLayout(t *testing.T) {
	cands := []AudioCandidate{
		{SourceMeta: SourceMeta{Asset: "a", Version: "v1", Track: 1}, Rate: 48000, Layout: "stereo"},
		{SourceMeta: SourceMeta{Asset: "a", Version: "v2", Track: 1}, Rate: 44100, Layout: "stereo"},
	}
	target := AudioSpec{Rate: 48000, Layout: "stereo", Channels: 2}
	dec, rejects := ResolveAudio("a", cands, target, true)
	if len(rejects) != 0 || dec == nil || len(dec.Conversions) != 1 {
		t.Fatalf("want one conversion, got dec=%+v rejects=%v", dec, rejects)
	}
	if dec.Conversions[0].From.Rate != 44100 || dec.Conversions[0].To.Rate != 48000 {
		t.Fatalf("conversion plan wrong: %+v", dec.Conversions[0])
	}
	// Divergent sources without an explicit target is a hard conflict.
	_, rejects = ResolveAudio("a", cands, AudioSpec{}, false)
	if len(rejects) != 1 || !errors.Is(rejects[0], ErrConflict) {
		t.Fatalf("want conflict without target, got %v", rejects)
	}
	// Unknown layout rejects only that track with a reason.
	bad := []AudioCandidate{
		{SourceMeta: SourceMeta{Asset: "a", Version: "v9", Track: 7}, Rate: 48000, Layout: "quad-wow"},
	}
	_, rejects = ResolveAudio("a", bad, target, true)
	if len(rejects) != 1 || !errors.Is(rejects[0], ErrUnknownLayout) ||
		rejects[0].Track != 7 || rejects[0].Msg == "" {
		t.Fatalf("unknown layout must be rejected with reason: %v", rejects)
	}
}

func TestResolveColorPriorityAndMissing(t *testing.T) {
	cands := []ColorCandidate{
		{SourceMeta: SourceMeta{Asset: "a", Version: "v1", Track: 2}, Prio: 1,
			Color: ColorInfo{Present: true, Primaries: 9, Transfer: 16, Matrix: 9}},
		{SourceMeta: SourceMeta{Asset: "a", Version: "v2", Track: 2}, Prio: 5,
			Color: ColorInfo{Present: true, Primaries: 1, Transfer: 1, Matrix: 1}},
	}
	res := ResolveColor(cands)
	if res.Missing || res.Picked == nil || res.Picked.Version != "v2" {
		t.Fatalf("higher priority must win: %+v", res.Picked)
	}
	if len(res.Dropped) != 1 || res.Dropped[0].Version != "v1" {
		t.Fatalf("discarded value must be recorded: %+v", res.Dropped)
	}
	// No usable color anywhere -> marked missing, never invented.
	res = ResolveColor([]ColorCandidate{{Color: ColorInfo{Present: false}}})
	if !res.Missing || res.Picked != nil {
		t.Fatal("absent color must be marked missing")
	}
	// Multiple fragments of one version cast a single vote, not multiple drops.
	multi := []ColorCandidate{
		{SourceMeta: SourceMeta{Asset: "a", Version: "v1", Track: 2}, Prio: 3,
			Color: ColorInfo{Present: true, Primaries: 1, Transfer: 1, Matrix: 1}},
		{SourceMeta: SourceMeta{Asset: "a", Version: "v1", Track: 2}, Prio: 3,
			Color: ColorInfo{Present: true, Primaries: 1, Transfer: 1, Matrix: 1}},
		{SourceMeta: SourceMeta{Asset: "a", Version: "v2", Track: 2}, Prio: 5,
			Color: ColorInfo{Present: true, Primaries: 9, Transfer: 16, Matrix: 9}},
	}
	res = ResolveColor(multi)
	if res.Picked == nil || res.Picked.Version != "v2" || len(res.Dropped) != 1 {
		t.Fatalf("per-version collapse wrong: picked=%+v dropped=%+v", res.Picked, res.Dropped)
	}
}

func TestMissingSeqs(t *testing.T) {
	items := []*Item{
		mkItem("a", "v1", 1, 0, "x", "processed"),
		mkItem("a", "v1", 1, 2, "x", "processed"),
		mkItem("a", "v1", 3, 5, "x", "processed"),
		mkItem("a", "v1", 3, 7, "x", "processed"),
	}
	miss := MissingSeqs(items)
	if len(miss[1]) != 1 || miss[1][0] != 1 {
		t.Fatalf("track1 holes wrong: %v", miss[1])
	}
	if len(miss[3]) != 1 || miss[3][0] != 6 {
		t.Fatalf("track3 holes wrong: %v", miss[3])
	}
}
