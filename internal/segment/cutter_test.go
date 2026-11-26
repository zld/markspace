package segment

import (
	"testing"
	"time"

	"pwflow/internal/timebase"
)

func ns(sec float64) int64 { return int64(sec * float64(time.Second)) }

func TestBuildCandidatesAndMergeOverlap(t *testing.T) {
	events := []Event{
		{ID: "e1", AtNS: ns(2)},
		{ID: "e2", AtNS: ns(3)},  // windows overlap e1
		{ID: "e3", AtNS: ns(8)},  // separate [6,11) clipped to 10s
		{ID: "e0", AtNS: ns(-1)}, // clamps to zero and remains valid
	}
	got := BuildCandidates(events, ns(10), DefaultPreNS, DefaultPostNS)
	if len(got) != 2 {
		t.Fatalf("candidates=%+v, want two merged ranges", got)
	}
	if got[0].Interval != (timebase.Interval{Lo: 0, Hi: ns(6)}) {
		t.Fatalf("first=%+v", got[0].Interval)
	}
	if got[1].Interval != (timebase.Interval{Lo: ns(6), Hi: ns(10)}) {
		t.Fatalf("second=%+v", got[1].Interval)
	}
	if len(got[0].FaultIDs) != 3 {
		t.Fatalf("merged fault IDs=%v", got[0].FaultIDs)
	}
}

func TestSnapNearestKeyframeWithin500ms(t *testing.T) {
	candidates := []Candidate{{Interval: timebase.Interval{Lo: ns(1.05), Hi: ns(5.04)}}}
	keyframes := []int64{0, ns(1), ns(2), ns(5), ns(6)}
	cuts := Snap(candidates, keyframes, DefaultKeyWindowNS)
	if len(cuts) != 1 {
		t.Fatalf("cuts=%d", len(cuts))
	}
	c := cuts[0]
	if c.Actual.Lo != ns(1) || c.Actual.Hi != ns(5) {
		t.Fatalf("actual=%+v, want 1s..5s", c.Actual)
	}
	if c.StartErrorNS != ns(0.05) || c.EndErrorNS != ns(0.04) || c.MaxErrorNS != ns(0.05) {
		t.Fatalf("errors start=%d end=%d max=%d", c.StartErrorNS, c.EndErrorNS, c.MaxErrorNS)
	}
	if c.StartKeyframeMiss || c.EndKeyframeMiss {
		t.Fatalf("unexpected miss flags: %+v", c)
	}
}

func TestSnapBeyondWindowRetainsRequestedBoundary(t *testing.T) {
	target := timebase.Interval{Lo: ns(1.6), Hi: ns(5.6)}
	cuts := Snap([]Candidate{{Interval: target}}, []int64{ns(1), ns(5)}, DefaultKeyWindowNS)
	c := cuts[0]
	if c.Actual != target {
		t.Fatalf("actual=%+v, want retained target %+v", c.Actual, target)
	}
	if !c.StartKeyframeMiss || !c.EndKeyframeMiss {
		t.Fatalf("expected keyframe misses: %+v", c)
	}
	if c.MaxErrorNS != ns(0.6) {
		t.Fatalf("max nearest-keyframe error=%d, want 600ms", c.MaxErrorNS)
	}
}
