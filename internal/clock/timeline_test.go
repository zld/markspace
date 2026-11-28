package clock

import "testing"

func TestConvertExactAndRounding(t *testing.T) {
	// 1s at 90kHz -> 90000 ticks at 48kHz -> 48000.
	got, err := Convert(90000, 90000, 48000)
	if err != nil || got != 48000 {
		t.Fatalf("Convert(1s) = %d, %v; want 48000", got, err)
	}
	// 3000 ticks at 90kHz = 1/30 s -> 1600 ticks at 48kHz exactly.
	got, _ = Convert(3000, 90000, 48000)
	if got != 1600 {
		t.Fatalf("frame convert = %d; want 1600", got)
	}
	// Nearest, ties away from zero: 1 tick at 3Hz -> 2Hz = 2/3 -> 1.
	got, _ = Convert(1, 3, 2)
	if got != 1 {
		t.Fatalf("round half = %d; want 1", got)
	}
	got, _ = Convert(-1, 3, 2)
	if got != -1 {
		t.Fatalf("negative round = %d; want -1", got)
	}
	if _, err := Convert(1, 0, 2); err == nil {
		t.Fatal("zero timescale must error")
	}
}

func TestConvertNoDriftAcrossChains(t *testing.T) {
	// Every frame of a 25fps track expressed in 90kHz ticks, bounced through
	// a 48kHz domain and back, must return the original tick for 10k frames.
	const frames = 10000
	for i := int64(0); i < frames; i++ {
		src := i * 3600 // 25fps on 90kHz
		mid, _ := Convert(src, 90000, 48000)
		back, _ := Convert(mid, 48000, 90000)
		if back != src {
			t.Fatalf("frame %d: drift %d -> %d -> %d", i, src, mid, back)
		}
	}
}

func TestRationalNormalized(t *testing.T) {
	r, err := NewRational(6, -4)
	if err != nil {
		t.Fatal(err)
	}
	if r.Num() != -3 || r.Den() != 2 {
		t.Fatalf("normalize sign/gcd = %s; want -3/2", r)
	}
	if _, err := NewRational(1, 0); err == nil {
		t.Fatal("zero denominator must error")
	}
}

func TestWrap16(t *testing.T) {
	// Rollover at the 16-bit boundary must keep the clock monotonic.
	prev := uint64(0xffff)
	got := Wrap16(prev, 0)
	if got != 0x10000 {
		t.Fatalf("wrap forward = %d; want 65536", got)
	}
	// Late packet just before rollover unwraps backwards.
	got = Wrap16(0x10005, 0xfffe)
	if got != 0xfffe {
		t.Fatalf("late packet = %d; want 65534", got)
	}
	if got := Wrap16(100, 101); got != 101 {
		t.Fatalf("in-order = %d; want 101", got)
	}
}

func TestWrap32(t *testing.T) {
	prev := uint64(0xffffffff)
	if got := Wrap32(prev, 0); got != 0x100000000 {
		t.Fatalf("32-bit wrap = %d; want 2^32", got)
	}
}
