package clock

import (
	"math"
	"testing"
)

func TestScaleRoundTrip(t *testing.T) {
	cases := []struct {
		name       string
		scale      Scale
		value      uint64
		wantMaster uint64
		wantBack   uint64
	}{
		{"90k identity", Scale{Num: 90000, Den: 1}, 180000, 180000, 180000},
		{"48k audio", Scale{Num: 48000, Den: 1}, 48000, 90000, 48000},
		{"44.1k audio", Scale{Num: 44100, Den: 1}, 44100, 90000, 44100},
		{"timescale 1", Scale{Num: 1, Den: 1}, 2, 180000, 2},
		// NTSC frame clock: 30000/1001 ticks per second, so 30000 ticks span 1001 s.
		{"fractional 30000/1001", Scale{Num: 30000, Den: 1001}, 30000, 90090000, 30000},
		{"fractional one tick", Scale{Num: 30000, Den: 1001}, 1, 3003, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.scale.ToMaster(tc.value)
			if err != nil {
				t.Fatalf("ToMaster: %v", err)
			}
			if got != tc.wantMaster {
				t.Fatalf("ToMaster = %d, want %d", got, tc.wantMaster)
			}
			back, err := tc.scale.FromMaster(got)
			if err != nil {
				t.Fatalf("FromMaster: %v", err)
			}
			if back != tc.wantBack {
				t.Fatalf("FromMaster = %d, want %d", back, tc.wantBack)
			}
		})
	}
}

func TestRoundingBoundaries(t *testing.T) {
	// 1 tick at 48k = 1.875 master ticks -> round to 2.
	s := Scale{Num: 48000, Den: 1}
	got, err := s.ToMaster(1)
	if err != nil || got != 2 {
		t.Fatalf("48k 1 tick = %d, err %v, want 2", got, err)
	}
	// 2 ticks = 3.75 -> round to 4.
	got, _ = s.ToMaster(2)
	if got != 4 {
		t.Fatalf("48k 2 ticks = %d, want 4", got)
	}
	// Millis rounding: 0.5 ms = 45 master ticks -> 1 ms.
	ms, err := ToMillis(45)
	if err != nil || ms != 1 {
		t.Fatalf("ToMillis(45) = %d, err %v, want 1", ms, err)
	}
	// 44 ticks -> 0 ms.
	if ms, _ := ToMillis(44); ms != 0 {
		t.Fatalf("ToMillis(44) = %d, want 0", ms)
	}
}

func TestZeroAndOverflow(t *testing.T) {
	if _, err := NewScale(0, 1); err == nil {
		t.Fatal("zero numerator must fail")
	}
	if _, err := NewScale(1, 0); err == nil {
		t.Fatal("zero denominator must fail")
	}
	bad := Scale{Num: 0, Den: 1}
	if _, err := bad.ToMaster(1); err == nil {
		t.Fatal("conversion with zero numerator must fail")
	}
	if _, err := bad.FromMaster(1); err == nil {
		t.Fatal("conversion with zero numerator must fail")
	}
	huge := Scale{Num: 1, Den: math.MaxUint64}
	if _, err := huge.ToMaster(math.MaxUint64); err == nil {
		t.Fatal("overflow must be reported, not wrapped")
	}
	if _, err := Add(math.MaxUint64, 1); err == nil {
		t.Fatal("uint64 add overflow must fail")
	}
	if err := CheckI64(uint64(math.MaxInt64) + 1); err == nil {
		t.Fatal("int64 range violation must fail")
	}
	if err := CheckI64(math.MaxInt64); err != nil {
		t.Fatalf("max int64 should be accepted: %v", err)
	}
}

func TestMillisHelpers(t *testing.T) {
	m, err := FromMillis(1000)
	if err != nil || m != 90000 {
		t.Fatalf("FromMillis = %d, err %v", m, err)
	}
	ms, err := ToMillis(m)
	if err != nil || ms != 1000 {
		t.Fatalf("round trip millis = %d, err %v", ms, err)
	}
}

func TestUnwrapTS(t *testing.T) {
	// 16-bit RTP sequence style wrap.
	prev, err := UnwrapTS(65534, 65534, 16)
	if err != nil || prev != 65534 {
		t.Fatalf("first = %d, err %v", prev, err)
	}
	next, err := UnwrapTS(prev, 65535, 16)
	if err != nil || next != 65535 {
		t.Fatalf("step = %d, err %v", next, err)
	}
	next, err = UnwrapTS(next, 0, 16)
	if err != nil || next != 65536 {
		t.Fatalf("wrap forward = %d, err %v, want 65536", next, err)
	}
	next, err = UnwrapTS(next, 1, 16)
	if err != nil || next != 65537 {
		t.Fatalf("post wrap = %d, err %v, want 65537", next, err)
	}
	// A small backwards move stays in the same epoch (reorder, not wrap).
	back, err := UnwrapTS(next, 65530, 16)
	if err != nil || back != 65530 {
		t.Fatalf("reorder = %d, err %v, want 65530", back, err)
	}
	// 32-bit RTP timestamp wrap near the boundary.
	ts32, err := UnwrapTS(0xFFFFFFFE, 0xFFFFFFFF, 32)
	if err != nil || ts32 != 0xFFFFFFFF {
		t.Fatalf("ts32 step = %x, err %v", ts32, err)
	}
	ts32, err = UnwrapTS(ts32, 0, 32)
	if err != nil || ts32 != 0x100000000 {
		t.Fatalf("ts32 wrap = %x, err %v, want 2^32", ts32, err)
	}
	// width 64 is identity.
	if v, err := UnwrapTS(10, math.MaxUint64, 64); err != nil || v != math.MaxUint64 {
		t.Fatalf("width64 = %d, err %v", v, err)
	}
	if _, err := UnwrapTS(0, 0, 0); err == nil {
		t.Fatal("width 0 must fail")
	}
	if _, err := UnwrapTS(0, 0, 65); err == nil {
		t.Fatal("width 65 must fail")
	}
}

func TestSeqDistance(t *testing.T) {
	if d := SeqDistance(65535, 0); d != 1 {
		t.Fatalf("wrap distance = %d, want 1", d)
	}
	if d := SeqDistance(0, 65535); d != -1 {
		t.Fatalf("back distance = %d, want -1", d)
	}
	if d := SeqDistance(100, 108); d != 8 {
		t.Fatalf("forward distance = %d, want 8", d)
	}
}

func TestGCDAndAbsDiff(t *testing.T) {
	if g := GCD(90000, 48000); g != 6000 {
		t.Fatalf("gcd = %d, want 6000", g)
	}
	if d := AbsDiff(10, 16); d != 6 {
		t.Fatalf("absdiff = %d, want 6", d)
	}
	if d := AbsDiff(16, 10); d != 6 {
		t.Fatalf("absdiff reversed = %d, want 6", d)
	}
}
