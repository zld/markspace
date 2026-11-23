package clock

import (
	"errors"
	"math"
	"testing"
)

func TestRateValidate(t *testing.T) {
	if err := Usec.Validate(); err != nil {
		t.Fatalf("usec should be valid: %v", err)
	}
	cases := []Rate{
		{Num: 0, Den: 1},
		{Num: 90000, Den: 0},
		{Num: 90000, Den: -1},
	}
	for i, r := range cases {
		err := r.Validate()
		if err == nil {
			t.Fatalf("case %d: expected error", i)
		}
		pe, ok := AsError(err)
		if !ok || pe.Phase != PhaseTimeline || pe.Recoverable {
			t.Fatalf("case %d: bad error model: %v", i, err)
		}
	}
}

func TestConvert(t *testing.T) {
	// 1 second of 90kHz RTP ticks -> 1,000,000 usec.
	got, err := Convert(90_000, RTPVideo, Usec)
	if err != nil || got != 1_000_000 {
		t.Fatalf("90k ticks -> usec = %d, %v", got, err)
	}
	// Non-zero origin mapping: half a second.
	got, err = Convert(45_000, RTPVideo, Usec)
	if err != nil || got != 500_000 {
		t.Fatalf("45k ticks -> usec = %d, %v", got, err)
	}
	// Milliseconds -> microseconds.
	if got, err = Convert(7, Msec, Usec); err != nil || got != 7000 {
		t.Fatalf("7 ms -> usec = %d, %v", got, err)
	}
	// Negative tick must fail with context.
	if _, err = Convert(-1, RTPVideo, Usec); err == nil {
		t.Fatal("negative tick accepted")
	}
	// Overflow must be reported, not wrapped.
	if _, err = Convert(math.MaxInt64, RTPVideo, Usec); err == nil {
		t.Fatal("overflow accepted")
	}
	// Zero denominator.
	if _, err = Convert(1, Rate{Num: 1, Den: 0}, Usec); err == nil {
		t.Fatal("zero denominator accepted")
	}
}

func TestUnwrap32(t *testing.T) {
	// Non-zero origin.
	prev := Begin32(4_000_000_000)
	prev, err := Unwrap32(prev, 4_000_000_090)
	if err != nil || prev != 4_000_000_090 {
		t.Fatalf("advance = %d, %v", prev, err)
	}
	// Wraparound 2^32-50 -> 40.
	prev = Begin32(0xFFFFFFCE)
	prev, err = Unwrap32(prev, 40)
	if err != nil || prev != 0x100000028 {
		t.Fatalf("wrap = %d, %v", prev, err)
	}
	// A genuine backward clock regression is an explicit error: absorbing it
	// as a wrap would shift the whole remaining timeline.
	prev = Begin32(900_000)
	if _, err = Unwrap32(prev, 10); err == nil {
		t.Fatal("backward clock regression accepted")
	}
	if pe, ok := AsError(err); !ok || pe.Recoverable || pe.Phase != PhaseTimeline {
		t.Fatalf("regression error model wrong: %v", err)
	}
	// Forward wraps chain correctly even when the origin is non-zero.
	prev = Begin32(0xFFFFFF00)
	prev, err = Unwrap32(prev, 0x00000010)
	if err != nil || prev != 0x100000010 {
		t.Fatalf("chained wrap = %#x, %v", prev, err)
	}
	prev, err = Unwrap32(prev, 0x00000120)
	if err != nil || prev != 0x100000120 {
		t.Fatalf("post-wrap advance = %#x, %v", prev, err)
	}
}

func TestUnwrap16(t *testing.T) {
	prev := int64(65534)
	prev = Unwrap16(prev, 65535)
	if prev != 65535 {
		t.Fatalf("step = %d", prev)
	}
	prev = Unwrap16(prev, 0) // wrap
	if prev != 65536 {
		t.Fatalf("wrap = %d", prev)
	}
	prev = Unwrap16(prev, 1)
	if prev != 65537 {
		t.Fatalf("post-wrap = %d", prev)
	}
	// Reordered (late) packet: negative delta.
	if got := Unwrap16(1000, 990); got != 990 {
		t.Fatalf("late = %d", got)
	}
}

func TestErrorModel(t *testing.T) {
	base := errors.New("disk full")
	err := E(PhaseJournal, 7, 42, true, "wal flush failed", base)
	if !errors.Is(err, base) {
		t.Fatal("Unwrap chain broken")
	}
	pe, ok := AsError(err)
	if !ok || pe.Track != 7 || pe.Seq != 42 || !pe.Recoverable {
		t.Fatalf("context lost: %v", err)
	}
	if s := err.Error(); s == "" || !contains(s, "phase=journal") {
		t.Fatalf("error text missing context: %q", s)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
