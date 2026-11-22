package fanout

import (
	"testing"

	"example.com/mediatimeline/internal/clock"
	"example.com/mediatimeline/internal/model"
)

func makeSamples(t *testing.T, n int, tickStep int64) []model.Sample {
	t.Helper()
	out := make([]model.Sample, 0, n)
	for i := 0; i < n; i++ {
		r, err := clock.FromTicks(int64(i)*tickStep, 90000)
		if err != nil {
			t.Fatal(err)
		}
		d, _ := clock.FromTicks(tickStep, 90000)
		out = append(out, model.Sample{
			Index: i, Track: model.TrackVideoMain,
			Time:     model.TimeStamp{RawTicks: int64(i) * tickStep, RepairedTime: clock.ToModel(r), State: model.DecodeExact},
			Duration: clock.ToModel(d), Size: 100, Sync: i%30 == 0,
		})
	}
	return out
}

func TestDefaultLadder(t *testing.T) {
	layers := DefaultLadder(3, 1920, 1080)
	if len(layers) != 3 {
		t.Fatalf("layers = %d", len(layers))
	}
	if layers[2].Width != 1920 || layers[2].Height != 1080 {
		t.Fatalf("top rung = %+v, want source size", layers[2])
	}
	if layers[0].Width >= layers[1].Width || layers[1].Width >= layers[2].Width {
		t.Fatalf("rungs are not increasing: %+v", layers)
	}
	for _, l := range layers {
		if l.Width%2 != 0 || l.Height%2 != 0 {
			t.Fatalf("rung %d has odd dimensions: %+v", l.Index, l)
		}
		if l.BitrateKbps < 100 {
			t.Fatalf("rung %d bitrate too low: %d", l.Index, l.BitrateKbps)
		}
	}
}

func TestBuildStridesAndSegments(t *testing.T) {
	samples := makeSamples(t, 90, 3000) // 3 s at 90 kHz
	specs := DefaultLadder(3, 1920, 1080)
	res, err := Build(specs, LayerInput{Track: model.TrackVideoMain, Timescale: 90000, Samples: samples}, 2000)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(res.Layers) != 3 {
		t.Fatalf("layers = %d", len(res.Layers))
	}
	// Layer i keeps every 2^(n-1-i)-th sample.
	wantCounts := []int{23, 45, 90} // ceil(90/4), ceil(90/2), 90
	for i, want := range wantCounts {
		if got := len(res.Layers[i].Samples); got != want {
			t.Fatalf("layer %d samples = %d, want %d", i, got, want)
		}
		if !res.Layers[i].Available {
			t.Fatalf("layer %d unexpectedly unavailable", i)
		}
		// Each layer plans two 2-second segments over 3 s of media.
		if got := len(res.Layers[i].SegmentStartTicks); got != 2 {
			t.Fatalf("layer %d segments = %d, want 2: %v", i, got, res.Layers[i].SegmentStartTicks)
		}
	}
}

func TestBuildIsolatesCorruptLayer(t *testing.T) {
	samples := makeSamples(t, 90, 3000)
	specs := DefaultLadder(3, 1920, 1080)
	corrupt := map[int]bool{}
	for i := range samples {
		corrupt[i] = true
	}
	res, err := Build(specs, LayerInput{
		Track: model.TrackVideoMain, Timescale: 90000, Samples: samples,
		CorruptIndices: corrupt, CorruptLayer: specs[2].Index,
	}, 2000)
	if err != nil {
		t.Fatalf("Build must not fail when a layer is damaged: %v", err)
	}
	if res.Layers[2].Available {
		t.Fatal("the damaged top layer must be unavailable")
	}
	if !res.Layers[0].Available || !res.Layers[1].Available {
		t.Fatal("a damaged layer must not contaminate the others")
	}
	if len(res.Layers[0].Samples) == 0 || len(res.Layers[1].Samples) == 0 {
		t.Fatal("healthy layers must retain their samples")
	}
	var sawLayerError bool
	for _, e := range res.Errors {
		if e.Code == model.CodeLayerUnavailable && e.Recoverable {
			sawLayerError = true
		}
	}
	if !sawLayerError {
		t.Fatalf("layer unavailability must be reported: %v", res.Errors)
	}
}

func TestBuildRejectsBadInput(t *testing.T) {
	if _, err := Build(nil, LayerInput{Timescale: 90000}, 2000); err == nil {
		t.Fatal("empty ladder should fail")
	}
	if _, err := Build(DefaultLadder(1, 0, 0), LayerInput{Timescale: 0}, 2000); err == nil {
		t.Fatal("zero timescale should fail")
	}
	if _, err := Build(DefaultLadder(1, 0, 0), LayerInput{Timescale: 90000}, 0); err == nil {
		t.Fatal("zero segment should fail")
	}
}

func TestBuildDeterministic(t *testing.T) {
	samples := makeSamples(t, 60, 3000)
	in := LayerInput{Track: model.TrackVideoMain, Timescale: 90000, Samples: samples}
	a, err := Build(DefaultLadder(3, 1920, 1080), in, 2000)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build(DefaultLadder(3, 1920, 1080), in, 2000)
	if err != nil {
		t.Fatal(err)
	}
	for i := range a.Layers {
		if len(a.Layers[i].SampleIndices) != len(b.Layers[i].SampleIndices) {
			t.Fatalf("layer %d is not deterministic", i)
		}
		for j := range a.Layers[i].SampleIndices {
			if a.Layers[i].SampleIndices[j] != b.Layers[i].SampleIndices[j] {
				t.Fatalf("layer %d sample %d differs between runs", i, j)
			}
		}
		if len(a.Layers[i].SegmentStartTicks) != len(b.Layers[i].SegmentStartTicks) {
			t.Fatalf("layer %d segment plan differs", i)
		}
	}
}
