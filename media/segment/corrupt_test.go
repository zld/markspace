package segment

import (
	"testing"

	gomp4mp4 "replaykit/media/mp4"
)

func truncLayer(t *testing.T, cut int) (*Layer, *gomp4mp4.Builder) {
	t.Helper()
	b := gomp4mp4.NewBuilder(90000, 1, 3000)
	b.WriteInit()
	for f := 0; f < 20; f++ {
		specs := make([]gomp4mp4.SampleSpec, 5)
		for i := range specs {
			specs[i] = gomp4mp4.SampleSpec{Duration: 3000, Key: i == 0, Payload: []byte("012345")}
		}
		b.AddFragment(uint64(f)*15000, uint32(f+1), specs)
	}
	b.TruncateLast(cut)
	data := b.Bytes()
	l := indexLayer(t, "damaged", data, 90000, 3000)
	return l, b
}

func TestTruncatedTailMakesPartialButRecoverable(t *testing.T) {
	l, _ := truncLayer(t, 30) // wipe final fragment mdat payload
	if l.Index.Complete {
		t.Fatal("fixture should be incomplete")
	}
	var truncated int
	for _, a := range l.Scan.Anomalies {
		if a.Category == "truncated_box" {
			truncated++
		}
	}
	if truncated != 1 {
		t.Fatalf("truncated_box anomalies=%d want 1", truncated)
	}
	// Event at 3.0s = tick 270000 (good frag19), asks for +0.5s into the gap.
	ev := Event{ID: "tail", AtNS: 3_000_000_000, PostNS: 500_000_000}
	w, err := PlanWindow(ev, l, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !w.Recoverable {
		t.Fatalf("highlight is intact, expected recoverable: %+v", w.Anomalies)
	}
	if !w.Partial || w.Uncovered == nil {
		t.Fatalf("expected partial + uncovered tail, partial=%v unc=%v", w.Partial, w.Uncovered)
	}
	if len(w.Bytes) == 0 {
		t.Fatal("valid prefix segment must still be written")
	}
}

func TestEventBeyondCoveredEndIsUnrecoverable(t *testing.T) {
	l, _ := truncLayer(t, 30) // covered end ~285000 ticks (3.166s)
	// Event at 4.0s lies past the truncation with no usable media.
	ev := Event{ID: "gone", AtNS: 4_000_000_000}
	w, err := PlanWindow(ev, l, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if w.Recoverable {
		t.Fatal("expected unrecoverable window for event beyond covered end")
	}
}

func TestCorruptKeyframeFragmentJumpsToNextKeyframe(t *testing.T) {
	data := buildLongClip(t, 90000, 3000, 40, 5)
	l := indexLayer(t, "corrupt", data, 90000, 3000)
	// tick 180000 -> 1-based fragment 13. Flag it corrupt externally.
	l.CorruptFragments = map[int]bool{13: true}
	ev := Event{ID: "jump", AtNS: 2_000_000_000} // start == 180000
	w, err := PlanWindow(ev, l, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !w.HasKeyframe || w.Keyframe.DecodeTime != 195000 {
		got := int64(-1)
		if w.HasKeyframe {
			got = w.Keyframe.DecodeTime
		}
		t.Fatalf("jumped keyframe=%d want 195000", got)
	}
	if w.Uncovered == nil {
		t.Fatal("expected uncovered skipped range")
	}
	sawResync := false
	for _, a := range w.Anomalies {
		if a.Category == "source_corrupt_skip" {
			sawResync = true
		}
	}
	if !sawResync {
		t.Fatalf("expected source_corrupt_skip anomaly: %+v", w.Anomalies)
	}
}
