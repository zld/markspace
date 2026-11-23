package repair

import (
	"errors"
	"io"
	"testing"

	"mediad/internal/testkit"
)

func TestRepairProducesMonotonicPerTrackTimestamps(t *testing.T) {
	doc, err := testkit.BuildDoc(testkit.DemoDocSpec(7))
	if err != nil {
		t.Fatal(err)
	}
	raw, tracks, err := demux(t, doc.Data)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("demux: %v", err)
	}
	res, err := Repair(raw, tracks, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateMonotonic(res.Blocks); err != nil {
		t.Fatalf("monotonicity: %v", err)
	}
	if len(res.Blocks) != len(raw) {
		t.Fatalf("blocks = %d, raw = %d", len(res.Blocks), len(raw))
	}
	// The audio device runs fast; repair must keep A/V within one audio
	// block of nominal.
	if max := res.MaxAVSkewNs(); max > 20*1e6 {
		t.Fatalf("max A/V skew = %.2f ms, want <= 20 ms", float64(max)/1e6)
	}
}

func TestRepairUnwrapsCrossingCounter(t *testing.T) {
	fixture, err := testkit.BuildFaulted(testkit.FaultTimestampWrap, 3)
	if err != nil {
		t.Fatal(err)
	}
	raw, tracks, err := demux(t, fixture.Data)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("demux: %v", err)
	}
	res, err := Repair(raw, tracks, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var unwraps int
	for _, d := range res.Decisions {
		if d.Kind == DecisionUnwrap {
			unwraps++
		}
	}
	if unwraps == 0 {
		t.Fatal("no unwrap decisions despite a wrapping counter")
	}
	if err := ValidateMonotonic(res.Blocks); err != nil {
		t.Fatalf("monotonicity after wrap: %v", err)
	}
	// The wrap must not send any track's time backwards across the whole
	// clip: the last block must be far later than the first.
	first := res.Blocks[0].TimeNs
	last := res.Blocks[len(res.Blocks)-1].TimeNs
	if last-first < int64(7*1e9) {
		t.Fatalf("clip spans %.2f s after unwrap, expected ~8 s",
			float64(last-first)/1e9)
	}
}

func TestRepairIsDeterministic(t *testing.T) {
	doc, err := testkit.BuildDoc(testkit.DemoDocSpec(11))
	if err != nil {
		t.Fatal(err)
	}
	run := func() *Result {
		raw, tracks, err := demux(t, doc.Data)
		if !errors.Is(err, io.EOF) {
			t.Fatalf("demux: %v", err)
		}
		res, err := Repair(raw, tracks, Options{})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	a, b := run(), run()
	if a.TimestampDigest() != b.TimestampDigest() {
		t.Fatal("timestamp digest differs across identical runs")
	}
	if a.DecisionDigest() != b.DecisionDigest() {
		t.Fatal("decision digest differs across identical runs")
	}
	if a.SummaryDigest() != b.SummaryDigest() {
		t.Fatal("summary digest differs across identical runs")
	}
}
