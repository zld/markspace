package pipeline

import (
	"testing"

	"mediasync/internal/model"
	"mediasync/internal/verify"
)

func baseLane(rep string, kind model.AssetKind, pri model.Priority, n int) LaneInput {
	segs := make([]model.SegmentDescriptor, n)
	for i := range segs {
		segs[i] = model.SegmentDescriptor{
			Source: "dash", Representation: rep, PeriodID: 0, Index: i, Kind: kind,
			Keyframe: kind == model.KindVideo && i%4 == 0,
			StartNS: int64(i) * 1_000_000, DurationNS: 1_000_000,
			Timescale: 1000, StartInScale: int64(i), DurationScale: 1,
			Payload: []byte{byte(i)},
		}
	}
	tracks, frags := mp4Tracks(rep, kind, n), mp4Frags(n)
	return LaneInput{
		Representation: rep, Kind: kind, Priority: pri, Segments: segs,
		MP4Tracks: tracks, MP4Fragments: frags,
	}
}

func mp4Tracks(rep string, kind model.AssetKind, n int) []verify.TrackSpec {
	tr := verify.TrackSpec{TrackID: 1, Timescale: 90000, Duration: uint64(n) * 90000, Kind: kind}
	if kind == model.KindVideo {
		for i := 0; i < n; i += 4 {
			tr.KeyframeIndices = append(tr.KeyframeIndices, uint32(i))
		}
	}
	return []verify.TrackSpec{tr}
}

func mp4Frags(n int) []verify.FragmentSpec {
	out := make([]verify.FragmentSpec, n)
	for i := range out {
		out[i] = verify.FragmentSpec{TrackID: 1, BaseDecodeTime: uint64(i) * 90000,
			SampleDuration: 90000, SampleCount: 1}
	}
	return out
}

func TestRunHealthyPipelineFullRates(t *testing.T) {
	lanes := []LaneInput{
		baseLane("dialogue", model.KindDialogue, model.PriorityCore, 8),
		baseLane("video", model.KindVideo, model.PriorityHigh, 8),
	}
	rep := Run(Config{BudgetMS: 1_000_000, MaxBytes: 1_000_000}, lanes)
	if rep.RoundTripRate != 1.0 {
		t.Fatalf("roundtrip rate = %.3f, want 1.000", rep.RoundTripRate)
	}
	if rep.KeyframeHitRate != 1.0 {
		t.Fatalf("keyframe hit rate = %.3f, want 1.000", rep.KeyframeHitRate)
	}
	if len(rep.SegmentHashes) != 16 {
		t.Fatalf("hashes = %d, want 16", len(rep.SegmentHashes))
	}
	for _, d := range rep.BudgetDecisions {
		if d.Action != "kept" {
			t.Fatalf("healthy lanes must be kept: %+v", d)
		}
	}
}

func TestBudgetSuspendsLowPriorityBeforeCore(t *testing.T) {
	lanes := []LaneInput{
		baseLane("dialogue", model.KindDialogue, model.PriorityCore, 8),
		baseLane("subtitle", model.KindSubtitle, model.PriorityCore, 8),
		baseLane("video-hi", model.KindVideo, model.PriorityHigh, 8),
		baseLane("video-lo", model.KindVideo, model.PriorityLow, 8),
	}
	// Tiny budget: core lanes (processed first) consume it, others suspend.
	rep := Run(Config{BudgetMS: 60, MaxBytes: 1_000_000}, lanes)

	kept, suspended := map[string]bool{}, map[string]string{}
	for _, d := range rep.BudgetDecisions {
		if d.Action == "kept" {
			kept[d.Representation] = true
		} else {
			suspended[d.Representation] = d.Action
		}
	}
	if !kept["dialogue"] || !kept["subtitle"] {
		t.Fatalf("core lanes must always remain complete: %+v", rep.BudgetDecisions)
	}
	if _, ok := suspended["video-hi"]; !ok {
		t.Fatal("high-priority video should suspend once budget is exhausted")
	}
	if _, ok := suspended["video-lo"]; !ok {
		t.Fatal("low-priority video should suspend once budget is exhausted")
	}
	for _, s := range rep.AlignedSegments {
		if s.Representation == "video-hi" || s.Representation == "video-lo" {
			t.Fatalf("suspended lane segments must not fan in: %+v", s)
		}
	}
}

func TestSizeCapAndCorruptLaneIsolation(t *testing.T) {
	lanes := []LaneInput{
		baseLane("dialogue", model.KindDialogue, model.PriorityCore, 8),
		baseLane("video", model.KindVideo, model.PriorityLow, 8),
	}
	lanes[1].CorruptMP4 = true
	tracks, frags := mp4Tracks("video", model.KindVideo, 8), mp4Frags(8)
	good, err := verify.WriteFragmentedMP4(tracks, frags)
	if err != nil {
		t.Fatal(err)
	}
	lanes[1].MP4Tracks, lanes[1].MP4Fragments = tracks, frags
	lanes[1].CorruptMP4Data = good[:len(good)/2]
	rep := Run(Config{BudgetMS: 1_000_000, MaxBytes: 1_000_000}, lanes)

	// The corrupt lane stays active but fails verification with a located
	// error, while the healthy lane still achieves its round trip.
	var foundTruncation bool
	for _, e := range rep.Errors {
		if model.Classify(e) == model.KindTruncatedMP4 && e.Subsystem == "verify" {
			foundTruncation = true
		}
	}
	if !foundTruncation {
		t.Fatalf("corrupt MP4 must be reported, errors: %+v", rep.Errors)
	}
	if rep.RoundTripRate != 0.5 {
		t.Fatalf("one of two lanes must still round-trip, got %.3f", rep.RoundTripRate)
	}

	// A size cap below the lanes' delivery size suspends the non-core lane.
	rep2 := Run(Config{BudgetMS: 1_000_000, MaxBytes: 10}, lanes)
	for _, d := range rep2.BudgetDecisions {
		if d.Representation == "video" && d.Action != "suspended-size" {
			t.Fatalf("size cap must suspend non-core lane: %+v", d)
		}
		if d.Representation == "dialogue" && d.Action != "kept" {
			t.Fatalf("core dialogue must survive the size cap: %+v", d)
		}
	}
}

func TestRunIsDeterministic(t *testing.T) {
	lanes := []LaneInput{
		baseLane("dialogue", model.KindDialogue, model.PriorityCore, 8),
		baseLane("video", model.KindVideo, model.PriorityHigh, 8),
	}
	cfg := Config{BudgetMS: 200, MaxBytes: 1_000_000}
	r1 := Run(cfg, lanes)
	r2 := Run(cfg, lanes)
	if len(r1.SegmentHashes) != len(r2.SegmentHashes) {
		t.Fatal("hash count differs across runs")
	}
	for i := range r1.SegmentHashes {
		if r1.SegmentHashes[i].SHA256 != r2.SegmentHashes[i].SHA256 {
			t.Fatalf("hash %d differs across runs", i)
		}
	}
	if r1.RoundTripRate != r2.RoundTripRate || r1.KeyframeHitRate != r2.KeyframeHitRate {
		t.Fatal("rates must be stable across runs")
	}
	if len(r1.BudgetDecisions) != len(r2.BudgetDecisions) {
		t.Fatal("decision count differs across runs")
	}
	for i := range r1.BudgetDecisions {
		if r1.BudgetDecisions[i] != r2.BudgetDecisions[i] {
			t.Fatalf("decision %d differs across runs", i)
		}
	}
}
