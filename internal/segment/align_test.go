package segment

import (
	"testing"

	"archive/internal/clock"
	"archive/internal/rtp"
)

func layer(name string, start, end int64) LayerInput {
	return LayerInput{
		Layer:           rtp.Layer{Name: name, TargetKbps: 900, FrameIntervalNS: 40000000},
		Result:          &rtp.Result{Ordered: []rtp.Ordered{{}, {}}},
		StartNS:         start,
		EndNS:           end,
		FrameIntervalNS: 40000000,
		ExpectedStartNS: 0,
		ExpectedEndNS:   400000000,
	}
}

func TestFanInAllAligned(t *testing.T) {
	sum, err := FanIn([]LayerInput{layer("a", 0, 400000000), layer("b", 0, 400000000)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Active != 2 || sum.Isolated != 0 || sum.ManifestAlignNS != 0 {
		t.Fatalf("summary = %+v", sum)
	}
	if sum.FrameCount != 20 {
		t.Fatalf("frames = %d", sum.FrameCount)
	}
}

func TestFanInIsolatesDriftingLayerAndKeepsOthers(t *testing.T) {
	bad := layer("bad", 0, 400000000)
	bad.StartNS = 2
	bad.EndNS = 400000002
	sum, err := FanIn([]LayerInput{layer("good", 0, 400000000), bad}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Active != 1 || sum.Isolated != 1 {
		t.Fatalf("active=%d isolated=%d", sum.Active, sum.Isolated)
	}
	var iso LayerStatus
	for _, l := range sum.Layers {
		if l.Isolated {
			iso = l
		}
	}
	if iso.Name != "bad" || iso.Reason == "" {
		t.Fatalf("isolation reason missing: %+v", iso)
	}
	if sum.ManifestAlignNS <= 1 {
		t.Fatalf("manifest alignment should record drift, got %d", sum.ManifestAlignNS)
	}
}

func TestFanInBoundaryExactlyOnGrid(t *testing.T) {
	// Boundary lands exactly between frames, still on a 1 ns grid line.
	in := layer("edge", 0, 400000001)
	in.ExpectedEndNS = 400000001
	sum, err := FanIn([]LayerInput{in}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if sum.ManifestAlignNS != 0 || sum.Isolated != 0 {
		t.Fatalf("boundary error not exact: %+v", sum)
	}
}

func TestFanInRejectsWhenAllIsolated(t *testing.T) {
	a := layer("a", 5, 400000005)
	b := layer("b", 9, 400000009)
	_, err := FanIn([]LayerInput{a, b}, 1)
	if clock.AsKind(err) != clock.KindMisaligned {
		t.Fatalf("kind = %q", clock.AsKind(err))
	}
}

func TestFanInRejectsEmpty(t *testing.T) {
	_, err := FanIn(nil, 1)
	if clock.AsKind(err) != clock.KindInvalidMetadata {
		t.Fatalf("kind = %q", clock.AsKind(err))
	}
}

func TestFanInIsolatesLayerWithNoPackets(t *testing.T) {
	empty := layer("empty", 0, 400000000)
	empty.Result = &rtp.Result{}
	sum, err := FanIn([]LayerInput{layer("ok", 0, 400000000), empty}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Active != 1 || sum.Isolated != 1 {
		t.Fatalf("summary = %+v", sum)
	}
}
