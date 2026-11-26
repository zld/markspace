package clock

import (
	"testing"

	"mediasync/internal/issue"
)

func TestUnwrapRTP(t *testing.T) {
	u := NewUnwrapper(1 << 32)
	start := uint64(0xFFFFFF00)
	var vals []uint64
	for i := 0; i < 6; i++ {
		raw := (start + uint64(i*1000)) & 0xFFFFFFFF
		r := u.Push(raw)
		if r.Raw != raw {
			t.Fatalf("raw changed: %x != %x", r.Raw, raw)
		}
		vals = append(vals, r.Value)
	}
	if u.Wraps() != 1 {
		t.Fatalf("wraps=%d", u.Wraps())
	}
	for i := 1; i < len(vals); i++ {
		if vals[i] <= vals[i-1] {
			t.Fatalf("not monotonic: %v", vals)
		}
	}
}

func TestUnwrapDuplicateAndRegress(t *testing.T) {
	u := NewUnwrapper(1 << 32)
	u.Push(100)
	if r := u.Push(100); !r.Duplicate {
		t.Fatal("duplicate not flagged")
	}
	u.Push(300)
	if r := u.Push(200); !r.Regressed {
		t.Fatal("regress not flagged")
	}
	u.Push(1000)
	if r := u.Push(4000); r.Missing != 2999 {
		t.Fatalf("missing=%d", r.Missing)
	}
}

func TestUnwrapRawNeverRewritten(t *testing.T) {
	u := NewUnwrapper(1 << 32)
	r := u.Push(0xFFFFFFFF)
	if r.Raw != 0xFFFFFFFF {
		t.Fatalf("raw=%x", r.Raw)
	}
	r = u.Push(5)
	if r.Raw != 5 || !r.Wrapped || r.Value != 0x100000005 {
		t.Fatalf("%+v", r)
	}
}

func issueLoc(in string) issue.Location {
	return issue.Location{Input: in, ByteOffset: -1}
}

func TestWatermarkWindow(t *testing.T) {
	w := NewWatermark(2 * Tick(TickRate))
	loc := issueLoc("a")
	if res, bad := w.Observe(10*Tick(TickRate), loc); res != Admitted || bad != nil {
		t.Fatalf("first: %v %v", res, bad)
	}
	// Advance well beyond the window.
	if res, _ := w.Observe(100*Tick(TickRate), loc); res != Admitted {
		t.Fatalf("advance: %v", res)
	}
	// Inside the window: late but replayable.
	if res, bad := w.Observe(99*Tick(TickRate), loc); res != LateReplay || bad != nil {
		t.Fatalf("late: %v %v", res, bad)
	}
	// Older than horizon: unrecoverable, with original horizon recorded.
	res, bad := w.Observe(5*Tick(TickRate), loc)
	if res != LateUnrecoverable || bad == nil {
		t.Fatalf("unrec: %v %v", res, bad)
	}
	if bad.Category != "late_event_unrecoverable" {
		t.Fatalf("cat=%s", bad.Category)
	}
	if w.Horizon() != 98*Tick(TickRate) {
		t.Fatalf("horizon=%s", w.Horizon())
	}
}

func TestDriftBound(t *testing.T) {
	var est DriftEstimator
	// 10 minutes with a 50 ms drift: inside the 100 ms bound.
	for i := 0; i <= 60; i++ {
		at := Tick(i) * 10 * Tick(TickRate)
		meas := at + Tick(i)*75 // 75 ticks per 10s step = 50ms over 10 min
		est.Add(at, meas)
	}
	c, ok := est.Analyze()
	if !ok || c.Exceeded {
		t.Fatalf("unexpected: %+v", c)
	}
	if c.Applied != c.Measured {
		t.Fatalf("applied=%s measured=%s", c.Applied, c.Measured)
	}
	// 10 minutes with 150 ms drift: exceeds, timestamps untouched.
	var est2 DriftEstimator
	for i := 0; i <= 60; i++ {
		at := Tick(i) * 10 * Tick(TickRate)
		meas := at + Tick(i)*225 // 150ms over 10 min
		est2.Add(at, meas)
	}
	c2, _ := est2.Analyze()
	if !c2.Exceeded {
		t.Fatalf("expected exceed: %+v", c2)
	}
	if c2.Applied != 0 {
		t.Fatalf("exceeded but applied=%s", c2.Applied)
	}
	if c2.OriginalLast != est2.samples[len(est2.samples)-1].Meas {
		t.Fatalf("original value changed")
	}
}

func TestDriftLimitPerTenMinutes(t *testing.T) {
	if got := DriftLimit(10 * 60 * Tick(TickRate)); got != Tick(100*(TickRate/1000)) {
		t.Fatalf("limit=%d", got)
	}
	if got := DriftLimit(20 * 60 * Tick(TickRate)); got != Tick(200*(TickRate/1000)) {
		t.Fatalf("limit2=%d", got)
	}
	if got := DriftLimit(0); got != 0 {
		t.Fatalf("limit0=%d", got)
	}
}
