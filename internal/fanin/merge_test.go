package fanin

import (
	"testing"

	"example.com/mediatimeline/internal/clock"
	"example.com/mediatimeline/internal/fanout"
	"example.com/mediatimeline/internal/model"
)

func layerSamples(t *testing.T, n int, fromTick, step int64) []model.Sample {
	t.Helper()
	out := make([]model.Sample, 0, n)
	for i := 0; i < n; i++ {
		r, err := clock.FromTicks(fromTick+int64(i)*step, 90000)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, model.Sample{
			Index: i, Track: model.TrackVideoMain,
			Time:     model.TimeStamp{RepairedTime: clock.ToModel(r), State: model.DecodeExact},
			Duration: model.RationalTime{Num: 1, Den: 30}, Size: 10,
		})
	}
	return out
}

func threeLayers(t *testing.T) fanout.Result {
	t.Helper()
	// 4 s of samples, one segment boundary at 2 s, one at 4 s.
	var layers []fanout.Layer
	for i := 0; i < 3; i++ {
		samples := layerSamples(t, 41, 0, 3000)
		layers = append(layers, fanout.Layer{
			Spec: fanout.LayerSpec{Index: i, Name: "rung"}, Track: model.TrackVideoMain,
			Timescale: 90000, Available: true, Samples: samples,
			SegmentStartTicks: []int64{0, 180000},
		})
	}
	return fanout.Result{Layers: layers}
}

func TestMergeHappyPath(t *testing.T) {
	res, err := Merge(threeLayers(t), Options{ToleranceTicks: 0, Timescale: 90000, SegmentTicks: 180000})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if len(res.Segments) != 2 {
		t.Fatalf("segments = %d, want 2: %+v", len(res.Segments), res.Segments)
	}
	if len(res.Recovered) != 3 || len(res.Unavailable) != 0 {
		t.Fatalf("recovered = %v unavailable = %v", res.Recovered, res.Unavailable)
	}
	if res.Segments[0].StartTick != 0 || res.Segments[1].StartTick != 180000 {
		t.Fatalf("segments = %+v", res.Segments)
	}
	if len(res.BoundaryErrors) != 0 {
		t.Fatalf("boundary errors = %+v", res.BoundaryErrors)
	}
}

func TestMergeExcludesUnavailableLayer(t *testing.T) {
	fan := threeLayers(t)
	fan.Layers[2].Available = false
	fan.Layers[2].Reason = "corrupt fragment"
	res, err := Merge(fan, Options{ToleranceTicks: 0, Timescale: 90000, SegmentTicks: 180000})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if len(res.Recovered) != 2 || len(res.Unavailable) != 1 || res.Unavailable[0] != 2 {
		t.Fatalf("recovered=%v unavailable=%v", res.Recovered, res.Unavailable)
	}
}

func TestMergeRequireAllLayersFails(t *testing.T) {
	fan := threeLayers(t)
	fan.Layers[1].Available = false
	_, err := Merge(fan, Options{ToleranceTicks: 0, Timescale: 90000, SegmentTicks: 180000, RequireAllLayers: true})
	if err == nil {
		t.Fatal("expected failure when all layers are required")
	}
	pe, ok := model.AsError(err)
	if !ok || pe.Code != model.CodeLayerUnavailable || pe.Recoverable {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestMergeBoundaryTolerance(t *testing.T) {
	fan := threeLayers(t)
	// The top layer's second boundary is 500 ticks late, inside a 1000-tick
	// tolerance.
	fan.Layers[2].SegmentStartTicks = []int64{0, 180500}
	if _, err := Merge(fan, Options{ToleranceTicks: 1000, Timescale: 90000, SegmentTicks: 180000}); err != nil {
		t.Fatalf("a boundary within tolerance must pass: %v", err)
	}
	// 2000 ticks late with a 1000-tick tolerance is a hard failure.
	fan.Layers[2].SegmentStartTicks = []int64{0, 182000}
	_, err := Merge(fan, Options{ToleranceTicks: 1000, Timescale: 90000, SegmentTicks: 180000})
	if err == nil {
		t.Fatal("expected a boundary mismatch")
	}
	pe, ok := model.AsError(err)
	if !ok || pe.Code != model.CodeBoundaryMismatch || pe.Recoverable {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestMergeShortLayerFails(t *testing.T) {
	fan := threeLayers(t)
	// A layer that misses the final segment would make the index
	// non-random-access at the tail.
	fan.Layers[1].SegmentStartTicks = []int64{0}
	_, err := Merge(fan, Options{ToleranceTicks: 1000, Timescale: 90000, SegmentTicks: 180000})
	if err == nil {
		t.Fatal("expected a short-layer failure")
	}
}

func TestMergeBadOptions(t *testing.T) {
	if _, err := Merge(fanout.Result{}, Options{}); err == nil {
		t.Fatal("zero timescale should fail")
	}
	if _, err := Merge(fanout.Result{}, Options{Timescale: 90000, SegmentTicks: 1000, ToleranceTicks: -1}); err == nil {
		t.Fatal("negative tolerance should fail")
	}
}

func TestMergeNoAvailableLayers(t *testing.T) {
	fan := threeLayers(t)
	for i := range fan.Layers {
		fan.Layers[i].Available = false
	}
	_, err := Merge(fan, Options{ToleranceTicks: 0, Timescale: 90000, SegmentTicks: 180000})
	if err == nil {
		t.Fatal("expected failure with no available layers")
	}
}

func TestSharedOrigin(t *testing.T) {
	fan := threeLayers(t)
	origin, err := SharedOrigin(fan)
	if err != nil {
		t.Fatal(err)
	}
	if !origin.IsZero() {
		t.Fatalf("origin = %s, want 0", origin)
	}
}
