package normalize

import (
	"errors"
	"testing"

	"mediarc/internal/model"
)

func TestResampleDeterministic44100To48000(t *testing.T) {
	in := make([]int16, 1470) // mono frames
	for i := range in {
		in[i] = int16((i * 17) % 32000)
	}
	a := Resample(in, 1, 44100, 48000)
	b := Resample(in, 1, 44100, 48000)
	if len(a) != 1600 {
		t.Fatalf("out frames=%d want 1600", len(a))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("resampling is not deterministic")
		}
	}
}

func TestExplicitMultiChannelMapping(t *testing.T) {
	layout51 := MustLayout("5.1")
	stereo := MustLayout("stereo")
	// One frame: FL=100, FR=200, FC=300, LFE=400, BL=500, BR=600.
	in := []int16{100, 200, 300, 400, 500, 600}
	routes := []Route{
		{From: "FL", To: "FL", GainQ15: UnityQ15},
		{From: "FR", To: "FR", GainQ15: UnityQ15},
		{From: "FC", To: "FL", GainQ15: 16384},
		{From: "FC", To: "FR", GainQ15: 16384},
	}
	out, err := MapChannels(in, layout51, stereo, routes)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0] != 250 || out[1] != 350 {
		t.Fatalf("mapped=%v want [250 350]", out)
	}
}

func TestUnknownChannelLayoutIsExit3(t *testing.T) {
	_, ok := LookupLayout("7.1-wide-surprise")
	if ok {
		t.Fatal("unknown layout accepted")
	}
	_, err := NormalizeAudio([]byte{0, 1, 0, 2}, AudioSpec{
		InputRate: 48000, OutputRate: 48000,
		Input: Layout{Name: "mystery", Channels: []string{"A", "B"}},
		Output: LayoutStereo,
	})
	var ee *model.ExitError
	if !errors.As(err, &ee) || ee.Code != model.ExitSyncConfig {
		t.Fatalf("err=%v want exit code 3", err)
	}
}

func TestColorConflictPreservesAllSources(t *testing.T) {
	sources := []model.SourcedColor{
		{Source: "container", Color: model.ColorMetadata{Primaries: "bt709", Transfer: "bt709"}},
		{Source: "bitstream", Color: model.ColorMetadata{Primaries: "bt2020", Transfer: "smpte2084"}},
	}
	faults := &model.Collector{}
	rep := ReconcileColor(sources, faults)
	if !rep.HasConflict || len(rep.Conflicts) != 2 {
		t.Fatalf("conflicts=%+v", rep.Conflicts)
	}
	if rep.Resolved.Primaries != "" || rep.Resolved.Transfer != "" {
		t.Fatalf("conflicted fields must not be resolved: %+v", rep.Resolved)
	}
	if sources[0].Color.Primaries != "bt709" || sources[1].Color.Primaries != "bt2020" {
		t.Fatal("source color values were overwritten")
	}
	if !faults.Has(model.FaultColorConflict) {
		t.Fatal("color conflict missing from fault model")
	}
}
