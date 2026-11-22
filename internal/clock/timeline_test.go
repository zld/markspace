package clock

import (
	"errors"
	"math"
	"testing"
)

func TestConvertExactAndRounded(t *testing.T) {
	cases := []struct {
		v        int64
		from, to uint32
		want     int64
	}{
		{0, 90000, 90000, 0},
		{1536, 48000, 90000, 2880}, // exact 48k->90k
		{90000, 90000, 1000, 1000}, // exact 90k->1k
		{1, 1000, 90000, 90},       // exact scale up
		{1, 90000, 1000, 0},        // 0.011 ms rounds down
		{5, 90000, 1000, 0},        // 0.055 -> 0? no: 0.055 rounds to 0
		{-3, 2, 1, -2},             // halves away from zero
		{3, 2, 1, 2},
		{math.MinInt64, 1, 1, math.MinInt64},
		{math.MaxInt64, 1, 1, math.MaxInt64},
	}
	for _, c := range cases {
		got, err := Convert(c.v, c.from, c.to)
		if err != nil {
			if c.v == 5 && c.from == 90000 {
				t.Fatalf("convert %d: unexpected err %v", c.v, err)
			}
			t.Fatalf("convert %d/%d->%d: %v", c.v, c.from, c.to, err)
		}
		if c.v == 5 && c.from == 90000 {
			if got != 0 {
				t.Fatalf("0.055ms should round to 0, got %d", got)
			}
			continue
		}
		if got != c.want {
			t.Fatalf("convert %d %d->%d = %d want %d", c.v, c.from, c.to, got, c.want)
		}
	}
}

func TestConvertErrors(t *testing.T) {
	if _, err := Convert(1, 0, 90000); !errors.Is(err, ErrZeroTimescale) {
		t.Fatalf("zero timescale: %v", err)
	}
	if _, err := Convert(1, 90000, 0); !errors.Is(err, ErrZeroTimescale) {
		t.Fatalf("zero target: %v", err)
	}
	if _, err := Convert(math.MaxInt64, 1, 90000); !errors.Is(err, ErrOverflow) {
		t.Fatalf("overflow not detected: %v", err)
	}
	var ce *ConversionError
	if _, err := Convert(-math.MaxInt64, 1, 2); !errors.As(err, &ce) {
		t.Fatalf("expected ConversionError, got %v", err)
	}
}

func TestUnwrapperHandlesWrapAndBackwards(t *testing.T) {
	u := &Unwrapper{}
	first := u.Push(0xFFFFFF00)
	if first.Extended != int64(0xFFFFFF00) {
		t.Fatalf("first extended = %d", first.Extended)
	}
	wrapped := u.Push(0x00000100)
	if !wrapped.Wrapped {
		t.Fatalf("expected wrap detection, got %+v", wrapped)
	}
	if wrapped.Extended != int64(0xFFFFFF00)+int64(0x200) {
		t.Fatalf("extended after wrap = %d", wrapped.Extended)
	}
	back := u.Push(0x00000080)
	if !back.Backwards || back.Delta >= 0 {
		t.Fatalf("expected backwards detection, got %+v", back)
	}
	wraps, backwards := u.Stats()
	if wraps != 1 || backwards != 1 {
		t.Fatalf("stats = %d/%d", wraps, backwards)
	}
}

func TestTimelineMappingAndNonMonotonic(t *testing.T) {
	tl, err := NewTimeline(1, 90000, 1000, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	m, err := tl.Map(1000)
	if err != nil {
		t.Fatal(err)
	}
	if m.Unified != 0 {
		t.Fatalf("epoch maps to 0, got %d", m.Unified)
	}
	m, err = tl.Map(19000)
	if err != nil {
		t.Fatal(err)
	}
	if m.Unified != 18000 {
		t.Fatalf("18k ticks at 90k = 18000, got %d", m.Unified)
	}
	m, err = tl.Map(10000)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Backwards {
		t.Fatalf("expected non-monotonic flag, got %+v", m)
	}
	st := tl.Stats()
	if st.Samples != 3 || st.NonMonotonic != 1 {
		t.Fatalf("stats %+v", st)
	}
}

func TestTimelineDriftCompensation(t *testing.T) {
	// sender runs 1000 ppm fast: 90090 ticks per second instead of 90000.
	tl, err := NewTimeline(2, 90090, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(0); i < 1000; i++ {
		ts := uint32(i * 90090 / 1000)
		if _, err := tl.Map(ts); err != nil {
			t.Fatal(err)
		}
	}
	est, err := NewDriftEstimator(90000)
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(0); i < 1000; i++ {
		est.Add(i*90090/1000, i*1e9/1000)
	}
	ppm, ok := est.PPM()
	if !ok {
		t.Fatal("drift fit failed")
	}
	if ppm > -900 || ppm < -1100 {
		t.Fatalf("fitted ppm = %d, want ~-1000 (sender fast)", ppm)
	}
}

func TestNewTimelineValidation(t *testing.T) {
	if _, err := NewTimeline(1, 0, 0, 0, 0); !errors.Is(err, ErrZeroTimescale) {
		t.Fatalf("zero timescale: %v", err)
	}
	if _, err := NewTimeline(1, 90000, 0, 0, 200000); err == nil {
		t.Fatal("out of range drift accepted")
	}
}

func TestCheckDeclaredDuration(t *testing.T) {
	if err := CheckDeclaredDuration(1000, 1000, 1000); err != nil {
		t.Fatalf("exact duration rejected: %v", err)
	}
	err := CheckDeclaredDuration(1000, 1000, 1001)
	if !errors.Is(err, ErrDurationOverflow) {
		t.Fatalf("expected ErrDurationOverflow, got %v", err)
	}
	var de *DurationError
	if !errors.As(err, &de) || de.Declared != 1000 || de.Actual != 1001 {
		t.Fatalf("bad duration error %+v", err)
	}
	if err := CheckDeclaredDuration(10, 0, 1); !errors.Is(err, ErrZeroTimescale) {
		t.Fatalf("zero timescale: %v", err)
	}
}

func TestTimelineAcrossFullTimestampWrap(t *testing.T) {
	tl, err := NewTimeline(1, 90000, 0xFFFFF000, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The epoch sits 0x1000 ticks before the 32-bit boundary, so mapping
	// 0x2000 after the wrap advances the timeline by 0x1000+0x2000 = 0x3000.
	if _, err := tl.Map(0xFFFFF000); err != nil {
		t.Fatal(err)
	}
	m, err := tl.Map(0x00002000)
	if err != nil {
		t.Fatal(err)
	}
	if m.Unified != 0x3000 {
		t.Fatalf("unified %d, want %d", m.Unified, 0x3000)
	}
	if !m.Wrapped {
		t.Fatalf("wrap not flagged: %+v", m)
	}
	if m.Backwards {
		t.Fatalf("wrapped sample marked backwards: %+v", m)
	}
	st := tl.Stats()
	if st.Wraps != 1 || st.NonMonotonic != 0 {
		t.Fatalf("stats %+v", st)
	}
}

func TestConvertRejectsInt64OverflowBothDirections(t *testing.T) {
	if _, err := Convert(math.MinInt64, 1, 90000); !errors.Is(err, ErrOverflow) {
		t.Fatalf("negative overflow missed: %v", err)
	}
	// 2^62 ticks at 1 Hz scaled into 90 kHz cannot fit in int64.
	if _, err := Convert(1<<62, 1, 90000); !errors.Is(err, ErrOverflow) {
		t.Fatalf("positive overflow missed: %v", err)
	}
	// Near-boundary values that do fit must convert exactly.
	v, err := Convert(1<<40, 1, 1)
	if err != nil || v != 1<<40 {
		t.Fatalf("identity conversion failed: %d %v", v, err)
	}
}
