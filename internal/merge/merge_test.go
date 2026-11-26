package merge

import (
	"testing"

	"mediamerge/internal/fault"
	"mediamerge/internal/track"
)

func cue(src, id string, start, end int64, text string) track.Cue {
	return track.Cue{ID: src + "#" + id, Index: 1, Start: clockTicks(start),
		End: clockTicks(end), Text: text, Source: src, Version: "v1"}
}

func TestDedupeIdenticalOverlap(t *testing.T) {
	in := []MappedTrack{
		{SourceID: "a.srt", Version: "v1", TargetID: "tl",
			Track: &track.Track{Cues: []track.Cue{cue("a.srt", "1", 0, 1, "hello")}}},
		{SourceID: "b.srt", Version: "v1", TargetID: "tl",
			Track: &track.Track{Cues: []track.Cue{cue("b.srt", "1", 0, 1, "hello")}}},
	}
	res, err := Merge(in, PolicyStrict)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Timelines["tl"].Cues) != 1 || res.Duplicates != 1 || len(res.Conflicts) != 0 {
		t.Fatalf("identical overlap must dedupe: %+v", res)
	}
}

func TestConflictStrict(t *testing.T) {
	in := []MappedTrack{
		{SourceID: "a.srt", TargetID: "tl",
			Track: &track.Track{Cues: []track.Cue{cue("a.srt", "1", 0, 2, "alpha")}}},
		{SourceID: "b.srt", TargetID: "tl",
			Track: &track.Track{Cues: []track.Cue{cue("b.srt", "1", 1, 3, "beta")}}},
	}
	res, err := Merge(in, PolicyStrict)
	if err == nil || fault.As(err).Kind != fault.KindConflict {
		t.Fatalf("strict policy must reject conflicts, got %v", err)
	}
	if len(res.Conflicts) != 1 || res.Conflicts[0].TargetTrack != "tl" {
		t.Fatalf("conflict not recorded: %+v", res.Conflicts)
	}
}

func TestConflictMarkGaps(t *testing.T) {
	in := []MappedTrack{
		{SourceID: "a.srt", TargetID: "tl",
			Track: &track.Track{Cues: []track.Cue{cue("a.srt", "1", 0, 2, "alpha")}}},
		{SourceID: "b.srt", TargetID: "tl",
			Track: &track.Track{Cues: []track.Cue{cue("b.srt", "1", 1, 3, "beta")}}},
	}
	res, err := Merge(in, PolicyMarkGaps)
	if err != nil || len(res.Conflicts) != 1 || len(res.Rejected) != 1 {
		t.Fatalf("mark-gaps keeps going: err=%v res=%+v", err, res)
	}
	if len(res.Timelines["tl"].Cues) != 1 || res.Timelines["tl"].Cues[0].Text != "alpha" {
		t.Fatalf("first cue must survive: %+v", res.Timelines["tl"].Cues)
	}
}

func TestGapRecordedWithProvenance(t *testing.T) {
	// a covers two disjoint ranges; b only mirrors the first (deduped), so b
	// has a provenance gap on the second range without any conflict.
	in := []MappedTrack{
		{SourceID: "a.srt", Version: "v1", TargetID: "tl", Track: &track.Track{Cues: []track.Cue{
			cue("a.srt", "1", 0, 1, "same"),
			cue("a.srt", "2", 3, 4, "later"),
		}}},
		{SourceID: "b.srt", Version: "v1", TargetID: "tl",
			Track: &track.Track{Cues: []track.Cue{cue("b.srt", "1", 0, 1, "same")}}},
	}
	res, err := Merge(in, PolicyMarkGaps)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Gaps) != 1 {
		t.Fatalf("want one gap, got %+v", res.Gaps)
	}
	g := res.Gaps[0]
	if g.Source != "b.srt" || g.Version != "v1" || g.TrackID != "tl" ||
		int64(g.Start) != 270000 || int64(g.End) != 360000 {
		t.Fatalf("gap provenance/range wrong: %+v", g)
	}
	if res.Duplicates != 1 {
		t.Fatalf("identical first cue must dedupe, got duplicates=%d", res.Duplicates)
	}
}

func TestNonOverlappingCuesAllKept(t *testing.T) {
	in := []MappedTrack{
		{SourceID: "a.srt", TargetID: "tl",
			Track: &track.Track{Cues: []track.Cue{cue("a.srt", "1", 0, 1, "one")}}},
		{SourceID: "b.srt", TargetID: "tl",
			Track: &track.Track{Cues: []track.Cue{cue("b.srt", "1", 2, 3, "two")}}},
	}
	res, err := Merge(in, PolicyStrict)
	if err != nil {
		t.Fatal(err)
	}
	cs := res.Timelines["tl"].Cues
	if len(cs) != 2 || cs[0].Start > cs[1].Start {
		t.Fatalf("cues must be kept and sorted: %+v", cs)
	}
}

func TestUnknownPolicy(t *testing.T) {
	if _, err := Merge(nil, Policy("nope")); err == nil {
		t.Fatal("unknown policy must error")
	}
}
