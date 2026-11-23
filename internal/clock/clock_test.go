package clock

import (
	"errors"
	"math"
	"math/big"
	"testing"
)

func TestRationalExactArithmetic(t *testing.T) {
	a := NewRational(1, 3)
	b := NewRational(2, 3)
	c := a.Add(b)
	if c.String() != "1" {
		t.Fatalf("1/3+2/3 = %s, want 1", c)
	}
	if a.Mul(NewRational(3, 7)).String() != "1/7" {
		t.Fatalf("(1/3)*(3/7) not reduced")
	}
	if a.Cmp(b) >= 0 {
		t.Fatalf("1/3 must be < 2/3")
	}
	if NewRational(4, 8).Equal(NewRational(1, 2)) == false {
		t.Fatal("reduced forms should be structurally equal")
	}
	if r := NewRational(7, 2).Round(); r.Int64() != 4 {
		t.Fatalf("round(7/2)=%d want 4", r.Int64())
	}
	if r := NewRational(6, 2).Floor(); r.Int64() != 3 {
		t.Fatalf("floor(3)=%d want 3", r.Int64())
	}
}

func TestRationalPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("zero denominator must panic")
		}
	}()
	NewRational(1, 0)
}

func TestTickConversionCommonTimescales(t *testing.T) {
	cases := []struct {
		value    int64
		scale    uint32
		wantTick int64
	}{
		{1, 90000, TickRate / 90000}, // RTP video clock
		{1, 48000, TickRate / 48000}, // audio
		{1, 1000, TickRate / 1000},   // millisecond timescale
		{3003, 30000, 100100000},     // 29.97fps style value
		{0, 44100, 0},
		{1 << 32, 90000, (1 << 32) * TickRate / 90000},
	}
	for _, c := range cases {
		got := ToTicks(c.value, c.scale)
		if got != c.wantTick {
			t.Errorf("ToTicks(%d,%d)=%d want %d", c.value, c.scale, got, c.wantTick)
		}
		back := FromTicks(got, c.scale)
		if back != c.value {
			t.Errorf("roundtrip(%d,%d)=%d", c.value, c.scale, back)
		}
	}
}

func TestTickConversionNoFloatAccumulation(t *testing.T) {
	// Exact-integral timescale (1kHz maps to 1e6 ticks per unit): zero drift.
	var exactSum int64
	for i := 0; i < 10000; i++ {
		exactSum += ToTicks(1, 1000)
	}
	if exactSum != ToTicks(10000, 1000) {
		t.Fatalf("integral timescale drifted: %d", exactSum)
	}
	// Non-integral timescales (90kHz, 44.1kHz): each conversion rounds at
	// most half a tick, so accumulated error stays within n/2 ticks of
	// the exact big.Rat reference — no float amplification allowed.
	for _, scale := range []uint32{90000, 44100} {
		var sum int64
		const n = 10000
		for i := 0; i < n; i++ {
			sum += ToTicks(1, scale)
		}
		ref := new(big.Rat).Mul(big.NewRat(n, 1), big.NewRat(TickRate, int64(scale)))
		refNum := new(big.Int).Quo(ref.Num(), ref.Denom()).Int64()
		if d := sum - refNum; d < -n/2 || d > n/2 {
			t.Fatalf("timescale %d drift %d exceeds per-sample rounding bound", scale, d)
		}
	}
}

func TestUnwrapperForwardAndDuplicate(t *testing.T) {
	u := NewUnwrapper()
	e0, err := u.Push(10, 1000, 0)
	if err != nil || e0 != 1000 {
		t.Fatalf("first push: %d %v", e0, err)
	}
	e1, _ := u.Push(11, 1400, 100)
	if e1 != 1400 {
		t.Fatalf("forward push = %d", e1)
	}
	eDup, _ := u.Push(11, 1400, 200)
	if eDup != 1400 {
		t.Fatalf("duplicate seq = %d, want 1400", eDup)
	}
}

func TestUnwrapperRolloverAccepted(t *testing.T) {
	u := NewUnwrapper()
	if _, err := u.Push(0, 0xFFFFFFF0, 0); err != nil {
		t.Fatal(err)
	}
	ext, err := u.Push(2, 0x20, 64) // 2 packets later, wrapped
	if err != nil {
		t.Fatalf("conclusive rollover rejected: %v", err)
	}
	want := int64(0xFFFFFFF0) + (0x100000000 - 0xFFFFFFF0) + 0x20
	if ext != want {
		t.Fatalf("ext=%d want %d", ext, want)
	}
	if u.Rollovers() != 1 {
		t.Fatalf("rollovers=%d want 1", u.Rollovers())
	}
}

func TestUnwrapperAmbiguousSmallBackJump(t *testing.T) {
	u := NewUnwrapper()
	_, _ = u.Push(0, 100000, 0)
	_, err := u.Push(1, 99000, 64) // backward jump inside ambiguity window
	var ae *AmbiguityError
	if !errors.As(err, &ae) {
		t.Fatalf("want AmbiguityError, got %v", err)
	}
	if ae.Offset != 64 || ae.Raw != 99000 {
		t.Fatalf("error lost provenance: %+v", ae)
	}
	if !errors.Is(err, ErrAmbiguousRollover) {
		t.Fatal("errors.Is must match ErrAmbiguousRollover")
	}
}

func TestUnwrapperAmbiguousLargeSeqGap(t *testing.T) {
	u := NewUnwrapper()
	_, _ = u.Push(0, 0xFFFFFFF0, 0)
	// Huge sequence gap with a wrapped-looking timestamp: cannot decide.
	_, err := u.Push(MaxSeqGap+10, 0x10, 128)
	var ae *AmbiguityError
	if !errors.As(err, &ae) {
		t.Fatalf("want AmbiguityError for large gap, got %v", err)
	}
	if u.Rollovers() != 0 {
		t.Fatal("failed push must not mutate rollover count")
	}
}

func TestUnwrapperReorderedPackets(t *testing.T) {
	u := NewUnwrapper()
	_, _ = u.Push(0, 1000, 0)
	_, _ = u.Push(1, 2000, 32)
	// Late packet from seq 65535 (previous cycle), raw 900.
	ext, err := u.Push(65535, 900, 64)
	if err != nil {
		t.Fatal(err)
	}
	if ext != 900 {
		t.Fatalf("reordered ext=%d want 900", ext)
	}
	// State must not have advanced.
	if u.Rollovers() != 0 {
		t.Fatal("reorder must not count as rollover")
	}
}

func TestUnwrapperMultipleRolloversStayMonotonic(t *testing.T) {
	u := NewUnwrapper()
	var last int64
	seq := uint16(0)
	raw := uint32(math.MaxUint32 - 100)
	ext, _ := u.Push(seq, raw, 0)
	last = ext
	for i := 0; i < 3; i++ {
		for j := 0; j < 300; j++ {
			seq++
			raw += 10_000_000 // ~429 packets per wrap cycle, conclusive wrap
			ext, err := u.Push(seq, raw, int64(i*300+j))
			if err != nil {
				t.Fatalf("step %d/%d: %v", i, j, err)
			}
			if ext < last {
				t.Fatalf("non-monotonic at %d: %d < %d", j, ext, last)
			}
			last = ext
		}
	}
	if u.Rollovers() < 1 {
		t.Fatal("expected at least one rollover")
	}
}
