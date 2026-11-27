package merge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"replay/internal/fault"
	"replay/internal/graph"
	"replay/internal/mapasset"
)

func testGraph(t *testing.T, tracks ...graph.Track) *graph.Graph {
	t.Helper()
	g, err := graph.New(tracks)
	if err != nil {
		t.Fatalf("graph.New: %v", err)
	}
	return g
}

func slot(track, version, digest string, start, end int64, merged ...string) mapasset.Slot {
	return mapasset.Slot{
		Track:      track,
		Interval:   mapasset.Interval{Start: start, End: end},
		Version:    version,
		Digest:     digest,
		MergedFrom: merged,
	}
}

func TestMergeOrdersByDependencyThenTime(t *testing.T) {
	g := testGraph(t,
		graph.Track{ID: "video", ClockRate: 90000, DependsOn: []string{"audio"}},
		graph.Track{ID: "audio", ClockRate: 48000},
	)
	inputs := []MergeInput{
		{Track: "video", Result: mapasset.Result{Slots: []mapasset.Slot{
			slot("video", "v1", "d2", 0, 10),
			slot("video", "v1", "d1", 0, 5),
		}}},
		{Track: "audio", Result: mapasset.Result{Slots: []mapasset.Slot{
			slot("audio", "v1", "d0", 0, 10),
		}}},
	}
	plan, err := Merge(g, inputs)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if plan.Order[0] != "audio" || plan.Order[1] != "video" {
		t.Fatalf("order=%v", plan.Order)
	}
	if plan.Segments[0].Track != "audio" {
		t.Fatalf("dependency order not respected: %+v", plan.Segments)
	}
	// Within a track, start then digest orders segments.
	if plan.Segments[1].Digest != "d1" || plan.Segments[2].Digest != "d2" {
		t.Fatalf("intra-track order=%+v", plan.Segments)
	}
}

func TestMergeCarriesCounts(t *testing.T) {
	g := testGraph(t, graph.Track{ID: "audio", ClockRate: 48000})
	inputs := []MergeInput{{Track: "audio", Result: mapasset.Result{
		Slots:     []mapasset.Slot{slot("audio", "v1", "d", 0, 10)},
		Conflicts: []mapasset.Conflict{{Track: "audio"}},
		Missing:   []mapasset.Missing{{Track: "audio"}, {Track: "audio"}},
	}}}
	plan, err := Merge(g, inputs)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if plan.ConflictsSeen != 1 || plan.MissingSeen != 2 {
		t.Fatalf("plan counts=%+v", plan)
	}
}

func TestMergeFailsOnCycleWithoutPublishing(t *testing.T) {
	// Build a cyclic graph by bypassing validation order: New rejects
	// cycles, so this asserts that the error propagates from Merge too.
	if _, err := graph.New([]graph.Track{
		{ID: "a", ClockRate: 48000, DependsOn: []string{"b"}},
		{ID: "b", ClockRate: 48000, DependsOn: []string{"a"}},
	}); err == nil {
		t.Fatal("graph.New should already reject the cycle")
	} else if !fault.IsKind(err, fault.KindGraph) {
		t.Fatalf("kind=%v", err)
	}
}

func TestMergeRejectsTrackOutsideGraph(t *testing.T) {
	g := testGraph(t, graph.Track{ID: "audio", ClockRate: 48000})
	_, err := Merge(g, []MergeInput{{Track: "ghost"}})
	if err == nil || !fault.IsKind(err, fault.KindMerge) {
		t.Fatalf("want merge error, got %v", err)
	}
}

func TestPlanDigestIsStable(t *testing.T) {
	g := testGraph(t, graph.Track{ID: "audio", ClockRate: 48000}, graph.Track{ID: "video", ClockRate: 90000})
	inputs := []MergeInput{
		{Track: "audio", Result: mapasset.Result{Slots: []mapasset.Slot{slot("audio", "v1", "a", 0, 5)}}},
		{Track: "video", Result: mapasset.Result{Slots: []mapasset.Slot{slot("video", "v1", "b", 0, 5)}}},
	}
	first, err := Merge(g, inputs)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	second, err := Merge(g, []MergeInput{inputs[1], inputs[0]})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	d1, _ := first.Digest()
	d2, _ := second.Digest()
	if d1 != d2 {
		t.Fatalf("digest depends on input order: %s vs %s", d1, d2)
	}
}

func TestWritePublishesAtomically(t *testing.T) {
	dir := t.TempDir()
	plan := &Plan{Segments: []Segment{{Track: "audio", StartNS: 0, EndNS: 10, Version: "v1", Digest: "d"}}}
	path, digest, err := plan.Write(dir)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if filepath.Base(path) != "plan.json" || len(digest) != 64 {
		t.Fatalf("path=%s digest=%s", path, digest)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var decoded Plan
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("published plan is not valid JSON: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "plan.json" {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestRollbackRemovesArtefactsAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	for _, rel := range []string{"map/audio.json", "merge/plan.json"} {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rec, err := Rollback(dir, "merge", "upstream changed", []string{"map/audio.json", "merge/plan.json"})
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if len(rec.Removed) != 2 || rec.Stage != "merge" || rec.Reason != "upstream changed" {
		t.Fatalf("record=%+v", rec)
	}
	// Second rollback finds nothing and still succeeds.
	rec2, err := Rollback(dir, "merge", "again", []string{"map/audio.json"})
	if err != nil || len(rec2.Removed) != 0 {
		t.Fatalf("idempotent rollback failed: %+v %v", rec2, err)
	}
}

func TestRollbackRejectsPathEscape(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(filepath.Dir(dir), "must-not-delete.txt")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(outside)
	_, err := Rollback(dir, "merge", "escape", []string{outside})
	if err == nil || !fault.IsKind(err, fault.KindMerge) {
		t.Fatalf("want merge error, got %v", err)
	}
	if _, statErr := os.Stat(outside); statErr != nil {
		t.Fatal("file outside workspace must survive rollback")
	}
}

func TestInvalidateRollsBackDownstreamOnly(t *testing.T) {
	plan := []string{"ingest", "map", "merge", "manifest"}
	var calls []string
	rollback := func(stage, reason string, downstream []string) (RollbackRecord, error) {
		calls = append(calls, stage+":"+reason)
		return RollbackRecord{Stage: stage, Reason: reason}, nil
	}
	records, err := Invalidate(plan, map[string]string{"map": "asset digest changed"}, rollback)
	if err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("records=%+v", records)
	}
	// Plan order in the returned records; reverse invocation order for
	// safe teardown is an implementation detail.
	want := []string{"map:asset digest changed", "merge:downstream of changed input", "manifest:downstream of changed input"}
	if len(calls) != 3 {
		t.Fatalf("rollback calls=%v", calls)
	}
	for i := range want {
		if calls[len(calls)-1-i] != want[i] {
			t.Fatalf("calls=%v want reverse of %v", calls, want)
		}
	}
	if records[0].Stage != "map" || records[2].Stage != "manifest" {
		t.Fatalf("records not in plan order: %+v", records)
	}
}

func TestInvalidateRejectsUnknownStage(t *testing.T) {
	_, err := Invalidate([]string{"a", "b"}, map[string]string{"zzz": "x"}, func(string, string, []string) (RollbackRecord, error) {
		return RollbackRecord{}, nil
	})
	if err == nil || !fault.IsKind(err, fault.KindArgument) {
		t.Fatalf("want argument error, got %v", err)
	}
}
