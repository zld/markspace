package pipeline_test

import (
	"testing"

	"mediasync/internal/fixture"
	"mediasync/internal/model"
	"mediasync/internal/pipeline"
	"mediasync/internal/report"
)

func runDemo(t *testing.T) report.Manifest {
	t.Helper()
	sc := fixture.Generate(17)
	target := model.TargetSpec{
		Channels: 2, SampleRate: 48000,
		Color: &model.ColorMeta{
			ColorPrimaries: 9, TransferCharacteristics: 16,
			MatrixCoefficients: 9, FullRange: false,
		},
		MaxBitrate: 96000, BudgetNS: 200_000_000,
	}
	return pipeline.Run(pipeline.Input{
		Tracks: sc.Tracks, Target: target, Seed: 17,
		Params: report.Params{BudgetNS: 200_000_000, MaxBitrate: 96000},
	})
}

func track(m report.Manifest, id string) *report.TrackReport {
	for i := range m.Tracks {
		if m.Tracks[i].TrackID == id {
			return &m.Tracks[i]
		}
	}
	return nil
}

func TestCleanTrackZeroDeltas(t *testing.T) {
	m := runDemo(t)
	dlg := track(m, "dialogue")
	if dlg == nil {
		t.Fatal("dialogue track missing")
	}
	if dlg.Status != report.StatusOK {
		t.Fatalf("dialogue status=%s failures=%+v", dlg.Status, dlg.Failures)
	}
	for _, mp := range dlg.Mappings {
		if mp.TimeDeltaNS != 0 || mp.ByteDelta != 0 {
			t.Fatalf("clean track must have zero deltas: %+v", mp)
		}
	}
}

func TestDuplicateAddsZeroTime(t *testing.T) {
	m := runDemo(t)
	sfx := track(m, "sfx")
	if sfx == nil {
		t.Fatal("sfx missing")
	}
	var dupes int
	var dupeDur model.NS
	for _, mp := range sfx.Mappings {
		if mp.Class == model.EntryDuplicate {
			dupes++
			dupeDur += mp.DurationNS
		}
	}
	if dupes == 0 {
		t.Fatal("expected duplicate entries")
	}
	if dupeDur != 0 {
		t.Fatalf("duplicate duration must be zero, got %d", dupeDur)
	}
}

func TestGapRecorded(t *testing.T) {
	m := runDemo(t)
	sfx := track(m, "sfx")
	var gaps int
	for _, mp := range sfx.Mappings {
		if mp.Class == model.EntryGap {
			gaps++
			if mp.Bytes != 0 || mp.SourceOffset != -1 {
				t.Fatalf("gap must carry no bytes/source: %+v", mp)
			}
		}
	}
	if gaps == 0 {
		t.Fatal("expected gap records for missing sequence")
	}
}

func TestTruncationLocatedByOffset(t *testing.T) {
	m := runDemo(t)
	for _, id := range []string{"broken", "aac-rtp"} {
		tr := track(m, id)
		if tr == nil {
			t.Fatalf("%s missing", id)
		}
		found := false
		for _, f := range tr.Failures {
			if f.PacketOffset == 4 || (id == "aac-rtp" && f.PacketOffset == 3) {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s failure not located at injected offset: %+v", id, tr.Failures)
		}
	}
}

func TestIndependentTracksStillProduce(t *testing.T) {
	m := runDemo(t)
	// broken/aac-rtp fail, but dialogue and aac-mp4 must still finish.
	for _, id := range []string{"dialogue", "aac-mp4", "sfx", "video"} {
		tr := track(m, id)
		if tr == nil {
			t.Fatalf("independent track %s missing", id)
		}
		if len(tr.Mappings) == 0 && id != "video" {
			t.Fatalf("track %s produced no mappings", id)
		}
	}
	aac := track(m, "aac-mp4")
	if aac == nil || aac.Status != report.StatusOK {
		t.Fatalf("MP4-backed AAC should succeed: %+v", aac)
	}
}

func TestDependencyCycleBlocked(t *testing.T) {
	m := runDemo(t)
	for _, id := range []string{"cyc-a", "cyc-b"} {
		tr := track(m, id)
		if tr == nil || tr.Status != report.StatusBlocked {
			t.Fatalf("%s must be blocked, got %+v", id, tr)
		}
		hasCycle := false
		for _, f := range tr.Failures {
			if f.Code == model.ErrGraphCycle {
				hasCycle = true
			}
		}
		if !hasCycle {
			t.Fatalf("%s missing graph-cycle failure: %+v", id, tr.Failures)
		}
		if tr.Decision == nil || tr.Decision.Eligible {
			t.Fatalf("%s must not get an eligible config", id)
		}
	}
}

func TestRerunDigestStable(t *testing.T) {
	first := runDemo(t)
	second := runDemo(t)
	if first.Digest != second.Digest {
		t.Fatalf("manifest digest changed across reruns:\n%s\n%s", first.Digest, second.Digest)
	}
	for i := range first.Tracks {
		if first.Tracks[i].Digest != second.Tracks[i].Digest {
			t.Fatalf("track %s digest changed", first.Tracks[i].TrackID)
		}
	}
}

func TestMappingsCarryInputToOutput(t *testing.T) {
	m := runDemo(t)
	dlg := track(m, "dialogue")
	if len(dlg.Mappings) != 8 {
		t.Fatalf("dialogue mappings=%d", len(dlg.Mappings))
	}
	// Output slots are zero-based and contiguous, each citing its source offset.
	for slot, mp := range dlg.Mappings {
		if mp.OutputSlot != slot || mp.SourceOffset != slot {
			t.Fatalf("mapping traceability broken at slot %d: %+v", slot, mp)
		}
	}
}

func TestVideoColorConflictReported(t *testing.T) {
	m := runDemo(t)
	vid := track(m, "video")
	if vid == nil || vid.Decision == nil {
		t.Fatal("video decision missing")
	}
	var colorSteps int
	for _, s := range vid.Decision.Steps {
		if len(s.Field) > 5 && s.Field[:5] == "color" {
			colorSteps++
		}
	}
	if colorSteps == 0 {
		t.Fatal("colour metadata conflict produced no conversion steps")
	}
}
