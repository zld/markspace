package policy_test

import (
	"errors"
	"math"
	"testing"

	"continuity/internal/manifest/policy"
	"continuity/internal/media"
)

func TestAudioRejectsUnknownLayout(t *testing.T) {
	_, err := policy.PlanAudio([]policy.AudioTrack{{ID: 9, Channels: 2, Layout: "mystery", LayoutKnown: false}}, 48000, false)
	if !errors.Is(err, media.ErrUnknownLayout) {
		t.Fatalf("err=%v", err)
	}
}

func TestFiveOneToStereoFixedMatrixAndNoLFEByDefault(t *testing.T) {
	plan, err := policy.PlanAudio([]policy.AudioTrack{{
		ID: 2, Channels: 6, Layout: "5.1", LayoutKnown: true, InputSampleRate: 44100,
	}}, 48000, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Mappings) != 1 {
		t.Fatalf("mappings=%d", len(plan.Mappings))
	}
	m := plan.Mappings[0]
	left := coefficientRow(t, m, "FL")
	if left["FL"] != 1.0 {
		t.Fatalf("FL coefficient=%v", left["FL"])
	}
	want := math.Pow(10, -3.0/20.0)
	if math.Abs(left["FC"]-want) > 1e-12 || math.Abs(left["SL"]-want) > 1e-12 {
		t.Fatalf("center/surround not -3dB: %+v want %v", left, want)
	}
	if _, ok := left["LFE"]; ok {
		t.Fatal("LFE must not be mixed by default")
	}
	if len(plan.SampleRates) != 1 || !plan.SampleRates[0].Resample || plan.SampleRates[0].DeltaHz != 3900 {
		t.Fatalf("sample rate decision=%+v", plan.SampleRates)
	}
}

func TestFiveOneMatrixCanExplicitlyMixLFE(t *testing.T) {
	plan, err := policy.PlanAudio([]policy.AudioTrack{{ID: 2, Channels: 6, Layout: "5.1", LayoutKnown: true}}, 48000, true)
	if err != nil {
		t.Fatal(err)
	}
	left := coefficientRow(t, plan.Mappings[0], "FL")
	if _, ok := left["LFE"]; !ok {
		t.Fatal("explicit LFE policy did not include LFE coefficient")
	}
}

func coefficientRow(t *testing.T, m policy.ChannelMapping, output string) map[string]float64 {
	t.Helper()
	for _, row := range m.Matrix {
		if row.Output == output {
			return row.Coefficients
		}
	}
	t.Fatalf("output %s not found", output)
	return nil
}

func TestColorPrioritySelectsFrame(t *testing.T) {
	plan, err := policy.ResolveColor([]policy.ColorObservation{
		{Field: "primaries", Value: "bt601", Level: policy.LevelContainer, Source: "c"},
		{Field: "primaries", Value: "bt709", Level: policy.LevelTrack, Source: "t"},
		{Field: "primaries", Value: "bt2020", Level: policy.LevelFrame, Source: "f"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Decisions[0].Value != "bt2020" || plan.Decisions[0].Source != "f" {
		t.Fatalf("decision=%+v", plan.Decisions[0])
	}
}

func TestColorSamePriorityConflictRequiresExplicitPolicy(t *testing.T) {
	obs := []policy.ColorObservation{
		{Field: "primaries", Value: "bt709", Level: policy.LevelFrame, Source: "sei-a"},
		{Field: "primaries", Value: "bt2020", Level: policy.LevelFrame, Source: "sei-b"},
	}
	_, err := policy.ResolveColor(obs, nil)
	if !errors.Is(err, media.ErrColorConflict) {
		t.Fatalf("err=%v", err)
	}
	var conflict *policy.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected ConflictError, got %T", err)
	}
	if len(conflict.Candidates) != 2 {
		t.Fatalf("candidates=%d", len(conflict.Candidates))
	}

	plan, err := policy.ResolveColor(obs, map[string]string{"primaries": "bt709"})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Decisions[0].ExplicitOverride || plan.Decisions[0].Value != "bt709" {
		t.Fatalf("explicit decision=%+v", plan.Decisions[0])
	}
}
