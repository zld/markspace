package clock

import (
	"math"
	"testing"

	"continuity/internal/cerr"
)

func TestToNanos(t *testing.T) {
	ns, err := ToNanos(1600, 1_000_000)
	if err != nil || ns != 1_600_000_000 {
		t.Fatalf("ns=%d err=%v", ns, err)
	}
	ns, err = ToNanos(-5, 1_000_000)
	if err != nil || ns != -5_000_000 {
		t.Fatalf("ns=%d err=%v", ns, err)
	}
}

func TestToNanosOverflow(t *testing.T) {
	if _, err := ToNanos(math.MaxInt64, 2); !cerr.Is(err, cerr.CodeTimestampOverflow) {
		t.Fatalf("err=%v, want overflow", err)
	}
	if _, err := ToNanos(math.MinInt64, 2); !cerr.Is(err, cerr.CodeTimestampOverflow) {
		t.Fatalf("err=%v, want overflow", err)
	}
	if _, err := ToNanos(1, 0); !cerr.Is(err, cerr.CodeTimestampOverflow) {
		t.Fatalf("err=%v, want zero-scale error", err)
	}
	// Boundary: exactly MaxInt64 must succeed.
	if ns, err := ToNanos(math.MaxInt64/2, 2); err != nil || ns != math.MaxInt64-1 {
		t.Fatalf("ns=%d err=%v", ns, err)
	}
}

func TestToNanosAtRate(t *testing.T) {
	// 882 units at 44100 Hz is exactly 20 ms.
	ns, err := ToNanosAtRate(882, 44100)
	if err != nil || ns != 20_000_000 {
		t.Fatalf("ns=%d err=%v", ns, err)
	}
	// Fractional remainder is exact integer arithmetic.
	ns, err = ToNanosAtRate(1, 3)
	if err != nil || ns != 333_333_333 {
		t.Fatalf("ns=%d err=%v", ns, err)
	}
	if _, err := ToNanosAtRate(1, 0); !cerr.Is(err, cerr.CodeTimestampOverflow) {
		t.Fatalf("err=%v, want zero-rate error", err)
	}
	if _, err := ToNanosAtRate(math.MaxInt64, 1); !cerr.Is(err, cerr.CodeTimestampOverflow) {
		t.Fatalf("err=%v, want overflow", err)
	}
}
