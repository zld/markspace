package clock

import (
	"testing"

	"github.com/pion/rtp"
)

func TestScaleTicksRounding(t *testing.T) {
	cases := []struct {
		v      int64
		from   uint32
		to     uint32
		expect int64
	}{
		{0, 90000, 1000, 0},
		{90000, 90000, 1000, 1000},
		{1, 3, 1, 0},   // 0.333 -> 0
		{2, 3, 1, 1},   // 0.666 -> 1
		{5, 2, 1, 3},   // 2.5 -> 3 (half away from zero)
		{-5, 2, 1, -3}, // -2.5 -> -3
		{48000, 48000, 90000, 90000},
		{1001, 24000, 90000, 3754},
	}
	for _, c := range cases {
		if got := ScaleTicks(c.v, c.from, c.to); got != c.expect {
			t.Fatalf("ScaleTicks(%d,%d,%d)=%d want %d", c.v, c.from, c.to, got, c.expect)
		}
	}
	if got := ScaleTicks(90000, 90000, 90000); got != 90000 {
		t.Fatalf("same timescale changed value: %d", got)
	}
}

func TestExtendAcrossWrap(t *testing.T) {
	// Start just below the wrap point. 0xFFFFFFF0 -> 0x00000100 is a
	// forward step of 0x110 ticks, not a backward step of ~4.29e9.
	u := &Unwrapper{}
	first, wrapped := u.Push(0xFFFFFFF0)
	if wrapped || first != 0xFFFFFFF0 {
		t.Fatalf("seed = %d/%v", first, wrapped)
	}
	next, wrapped := u.Push(0x00000100)
	if !wrapped {
		t.Fatal("expected wrap flag")
	}
	if next != 0x100000100 {
		t.Fatalf("extended = %#x want %#x", next, 0x100000100)
	}
	if delta := next - first; delta != 0x110 {
		t.Fatalf("delta = %#x want %#x", delta, 0x110)
	}
	// A value behind the current position but within half the range stays on
	// the same revolution and is not reported as a wrap.
	back, wrapped := u.Push(0xFFFFFF00)
	if wrapped {
		t.Fatal("backward step must not be reported as a wrap")
	}
	if back != 0xFFFFFF00 {
		t.Fatalf("back = %#x", back)
	}
	if u.Wraps() != 1 {
		t.Fatalf("wraps = %d", u.Wraps())
	}
}

func TestExtendStableWithoutWrap(t *testing.T) {
	prev := int64(1000)
	if v, w := Extend(prev, 2000); w || v != 2000 {
		t.Fatalf("forward: %d %v", v, w)
	}
	if v, w := Extend(prev, 900); w || v != 900 {
		t.Fatalf("small backward: %d %v", v, w)
	}
}

func TestRTPHeaderInterop(t *testing.T) {
	want := uint32(0xDEADBEEF)
	h := HeaderForRaw(want)
	buf := make([]byte, 64)
	n, err := h.MarshalTo(buf)
	if err != nil {
		t.Fatal(err)
	}
	var got rtp.Header
	if _, err := got.Unmarshal(buf[:n]); err != nil {
		t.Fatal(err)
	}
	if RawFromHeader(got) != want {
		t.Fatalf("timestamp = %#x want %#x", RawFromHeader(got), want)
	}
	if d := SignedDelta(0x00000010, 0xFFFFFFF0); d != 0x20 {
		t.Fatalf("signed delta = %d", d)
	}
}

func TestTimebaseValidation(t *testing.T) {
	if _, err := New(0); err == nil {
		t.Fatal("expected error for zero timescale")
	}
	tb, err := New(90000)
	if err != nil {
		t.Fatal(err)
	}
	if got := tb.ToMedia(9000, 1000); got != 100 {
		t.Fatalf("ToMedia = %d", got)
	}
	if got := tb.FromMedia(100, 1000); got != 9000 {
		t.Fatalf("FromMedia = %d", got)
	}
	if got := tb.SecondsToTicks(2); got != 180000 {
		t.Fatalf("SecondsToTicks = %d", got)
	}
}
