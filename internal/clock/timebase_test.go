package clock

import (
	"math"
	"testing"
)

func TestNewRationalRejectsBadDenominator(t *testing.T) {
	if _, err := NewRational(1, 0); err == nil {
		t.Fatal("zero denominator must be rejected")
	}
	r, err := NewRational(90000, 30000)
	if err != nil {
		t.Fatal(err)
	}
	if r.Num != 3 || r.Den != 1 {
		t.Fatalf("ratio not reduced: %v", r)
	}
}

func TestZeroAndNegativeTimescaleRejected(t *testing.T) {
	for _, v := range []int64{0, -1} {
		tb, err := New(v)
		if err == nil || tb.Valid() {
			t.Fatalf("timescale %d must be invalid", v)
		}
	}
}

func TestConvertRTPToMP4AndMaster(t *testing.T) {
	rtp, _ := New(90000)
	mp4, _ := New(9000)
	// 3000 RTP ticks = 1/30 s = 300 MP4 ticks
	got := rtp.At(3000).Convert(mp4)
	if got.Ticks() != 300 {
		t.Fatalf("cross-timebase conversion = %d, want 300", got.Ticks())
	}
	nano := rtp.At(90000).Convert(Master)
	if nano.Ticks() != 1_000_000_000 {
		t.Fatalf("one second in nanos = %d", nano.Ticks())
	}
}

func TestNonIntegerRateExact(t *testing.T) {
	// A fractional 90000/1001 NTSC rate must keep frame timing exact.
	tb, err := NewFromRational(Rational{Num: 90000, Den: 1001})
	if err != nil {
		t.Fatal(err)
	}
	oneFrame := tb.At(3003).Convert(Master) // 3003 ticks = 3003*1001/90000 s
	want := int64(33_400_033_333)           // 33.400033... ms in nanos
	if d := oneFrame.Ticks() - want; d < -2 || d > 2 {
		t.Fatalf("fractional frame = %d, want ~%d", oneFrame.Ticks(), want)
	}
}

func TestCmpAndDelta(t *testing.T) {
	a, _ := New(48000)
	b, _ := New(90000)
	// 48000 audio ticks and 90000 video ticks are both exactly 1 second.
	if Cmp(a.At(48000), b.At(90000)) != 0 {
		t.Fatal("equal physical time compared unequal")
	}
	ns, ok := DeltaNanos(a.At(0), b.At(9000))
	if !ok || ns != 100_000_000 {
		t.Fatalf("delta ns = %d ok=%v", ns, ok)
	}
	d, ok := Since(a.At(0), a.At(48))
	if !ok || d.Milliseconds() != 1 {
		t.Fatalf("duration = %v ok=%v", d, ok)
	}
}

func TestNonZeroStartAndNegativeTicks(t *testing.T) {
	tb, _ := New(1000)
	first := tb.At(5000)
	second := tb.At(5100)
	if DeltaMillis(first, second) != 100 {
		t.Fatal("non-zero start offsets must still yield correct deltas")
	}
	neg := tb.At(-100).Convert(Master)
	if neg.Ticks() >= 0 {
		t.Fatalf("negative tick = %d, want negative nanos", neg.Ticks())
	}
}

func TestWraparound32(t *testing.T) {
	w, err := NewWraparound(32)
	if err != nil {
		t.Fatal(err)
	}
	max := uint64(math.MaxUint32)
	if v := w.Extend(max - 2); v != int64(max-2) {
		t.Fatalf("first extend = %d", v)
	}
	if v := w.Extend(3); v != int64(max)+4 {
		t.Fatalf("wrap forward = %d, want %d", v, int64(max)+4)
	}
	// reverse jump larger than half range => backwards without wrap
	if v := w.Extend(1); v != int64(max)+2 {
		t.Fatalf("small backstep = %d", v)
	}
}

func TestWraparound16Sequence(t *testing.T) {
	w, _ := NewWraparound(16)
	w.Extend(65534)
	if v := w.Extend(1); v != 65537 {
		t.Fatalf("16-bit wrap = %d, want 65537", v)
	}
	if _, err := NewWraparound(0); err == nil {
		t.Fatal("zero width must error")
	}
}
