package track

import (
	"testing"

	"mediarc/internal/model"
)

func testTracks() []model.Track {
	return []model.Track{
		{ID: 1, Type: model.TrackVideo, Codec: model.CodecSyntheticVideo},
		{ID: 2, Type: model.TrackAudio, Codec: model.CodecSyntheticAudio, SampleRate: 48000, Layout: model.LayoutStereo, Channels: 2},
		{ID: 3, Type: model.TrackAudio, Codec: model.CodecSyntheticAudio, SampleRate: 44100, Layout: model.Layout51, Channels: 6},
	}
}

func TestPlanMappingSuccessAndSpec(t *testing.T) {
	rules, err := ParseSpec("1:10, 2:20")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanMapping(testTracks(), rules)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Mappings) != 2 || plan.Mappings[0].TargetID != 10 {
		t.Fatalf("unexpected plan: %+v", plan.Mappings)
	}
}

func TestDuplicateTargetRejected(t *testing.T) {
	rules := []Rule{{SourceID: 1, TargetID: 9}, {SourceID: 2, TargetID: 9}}
	_, err := PlanMapping(testTracks(), rules)
	if !model.HasCode(err, model.CodeDuplicateTarget) {
		t.Fatalf("want duplicate target, got %v", err)
	}
}

func TestMissingRequiredRejected(t *testing.T) {
	rules := []Rule{{SourceID: 40, TargetID: 40, Required: true}}
	_, err := PlanMapping(testTracks(), rules)
	if !model.HasCode(err, model.CodeMissingRequired) {
		t.Fatalf("want missing required, got %v", err)
	}
}

func TestIncompatibleAudioRejectedWithoutConversion(t *testing.T) {
	rules := []Rule{{SourceID: 2, TargetID: 2, SampleRate: 44100}}
	_, err := PlanMapping(testTracks(), rules)
	if !model.HasCode(err, model.CodeRateMismatch) {
		t.Fatalf("want rate mismatch, got %v", err)
	}
	rules = []Rule{{SourceID: 2, TargetID: 2, Layout: model.Layout51}}
	_, err = PlanMapping(testTracks(), rules)
	if !model.HasCode(err, model.CodeLayoutMismatch) {
		t.Fatalf("want layout mismatch, got %v", err)
	}
}
