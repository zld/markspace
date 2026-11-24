package remux_test

import (
	"bytes"
	"testing"

	"mediarc/internal/fixture"
	"mediarc/internal/model"
	"mediarc/internal/remux"
	"mediarc/internal/track"
	"mediarc/internal/verify"
)

func mappedPlan(t *testing.T, a *model.Archive) *track.Plan {
	t.Helper()
	plan, err := track.PlanMapping(a.Tracks, track.DefaultRules(a.Tracks))
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestLosslessRemuxIsDeterministicAndComparable(t *testing.T) {
	a := fixture.GenerateArchive(fixture.DefaultOptions())
	plan := mappedPlan(t, a)

	r1, err := remux.Remux(a, plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := remux.Remux(a, plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(r1.Bytes, r2.Bytes) || r1.SHA256 != r2.SHA256 {
		t.Fatal("remux output is not deterministic")
	}
	cmp, err := verify.CompareRemux(a, r1.Bytes, plan)
	if err != nil {
		t.Fatalf("comparison failed: %v mismatches=%v", err, cmp.Mismatches)
	}
	if !cmp.SampleHashesMatch || !cmp.PropertiesMatch {
		t.Fatalf("lossless verification failed: %+v", cmp.Mismatches)
	}
	if cmp.InputSampleCount != 78 || cmp.OutputSampleCount != 78 {
		t.Fatalf("sample counts %d/%d", cmp.InputSampleCount, cmp.OutputSampleCount)
	}
}

func TestRemuxRejectsColorConflict(t *testing.T) {
	opt := fixture.DefaultOptions()
	opt.ColorConflict = true
	a := fixture.GenerateArchive(opt)
	plan := mappedPlan(t, a)
	r, err := remux.Remux(a, plan, nil)
	if r != nil {
		t.Fatal("no result should be returned for ambiguous color")
	}
	if !model.HasCode(err, model.CodeColorConflict) {
		t.Fatalf("want color conflict, got %v", err)
	}
	me, _ := model.AsMediaError(err)
	if len(me.Fields) == 0 {
		t.Fatal("conflict fields were not retained")
	}
}

func TestRemuxRejectsInconsistentAudioLayout(t *testing.T) {
	a := fixture.GenerateArchive(fixture.DefaultOptions())
	a.Track(2).Channels = 1 // contradicts stereo layout
	plan := mappedPlan(t, a)
	_, err := remux.Remux(a, plan, nil)
	if !model.HasCode(err, model.CodeLayoutMismatch) {
		t.Fatalf("want layout consistency error, got %v", err)
	}
}

func TestExplicitTargetMappingPreservesSamples(t *testing.T) {
	a := fixture.GenerateArchive(fixture.DefaultOptions())
	rules := []track.Rule{
		{SourceID: 1, TargetID: 101, Type: model.TrackVideo, Required: true},
		{SourceID: 2, TargetID: 202, Type: model.TrackAudio, Required: true},
	}
	plan, err := track.PlanMapping(a.Tracks, rules)
	if err != nil {
		t.Fatal(err)
	}
	r, err := remux.Remux(a, plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Archive.Track(101) == nil || r.Archive.Track(202) == nil {
		t.Fatalf("target tracks missing: %+v", r.Archive.Tracks)
	}
	if len(r.Archive.SamplesFor(101)) != 30 || len(r.Archive.SamplesFor(202)) != 48 {
		t.Fatal("mapped sample counts changed")
	}
}
