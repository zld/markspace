package segment

import (
	"testing"

	"replaykit/media/fault"
)

func TestFanoutFlagsSkewBeyondOneFrame(t *testing.T) {
	// Reference 30fps grid: keyframes every 15000 ticks. Coarse 15fps grid with
	// 6000-tick frames x5: keyframes every 30000 ticks. At a 1.2s start the
	// chosen keyframes differ by 15000 ticks, far beyond one 3000-tick frame.
	ref := buildLongClip(t, 90000, 3000, 40, 5)
	coarse := buildLongClip(t, 90000, 6000, 40, 5)
	layers := []*Layer{
		indexLayer(t, "fine", ref, 90000, 3000),
		indexLayer(t, "coarse", coarse, 90000, 6000),
	}
	ev := Event{ID: "skew", AtNS: 2_000_000_000, PreNS: 800_000_000} // start 1.2s
	res := Fanout([]Event{ev}, layers, Options{})

	if res.MaxSkewTicks <= 3000 {
		t.Fatalf("max skew=%d expected > one frame 3000", res.MaxSkewTicks)
	}
	sawSkew := false
	for _, a := range res.Anomalies {
		if a.Category == fault.CategoryBoundarySkew {
			sawSkew = true
		}
	}
	if !sawSkew {
		t.Fatalf("expected boundary_skew anomaly, got %+v", res.Anomalies)
	}
	for _, b := range res.Boundaries {
		if b.Layer == "coarse" && b.WithinFrame {
			t.Fatalf("coarse boundary %+v must be outside one frame", b)
		}
	}
	var coarseWin *Window
	for _, w := range res.Windows {
		if w.LayerName == "coarse" {
			coarseWin = w
		}
	}
	if coarseWin == nil || !coarseWin.Partial {
		t.Fatal("coarse window must be marked partial on excessive skew")
	}
}
