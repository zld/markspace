package media

import (
	"testing"
	"time"
)

func TestTimebaseConversionAndRange(t *testing.T) {
	r, err := NewRange(Timebase48k, 480, 960)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Duration(); got != 20*time.Millisecond {
		t.Fatalf("duration=%v want 20ms", got)
	}
	ms, err := Timebase48k.Convert(960, TimebaseMP4k)
	if err != nil {
		t.Fatal(err)
	}
	if ms != 20 {
		t.Fatalf("converted=%dms want 20", ms)
	}
}

func TestInvalidTimestampIsStructuredNotZeroed(t *testing.T) {
	_, err := NewRange(Timebase48k, -1, 10)
	if err == nil || !HasCode(err, CodeTimestampNegative) {
		t.Fatalf("err=%v want TIMESTAMP_NEGATIVE", err)
	}
	e, _ := AsError(err)
	if e.Message == "" {
		t.Fatal("structured error should retain message")
	}

	bad := TimeRange{
		Start: Timestamp{Ticks: 100, Timebase: Timebase48k},
		End:   Timestamp{Ticks: 90, Timebase: Timebase48k},
	}
	if err := ValidateRange(bad); !HasCode(err, CodeTimestampNonMonotonic) {
		t.Fatalf("ValidateRange err=%v want non-monotonic", err)
	}
}

func TestCheckAdjacentAllowsGapButRejectsOverlap(t *testing.T) {
	prevEnd := Timestamp{Ticks: 1000, Timebase: Timebase48k}
	nextStart := Timestamp{Ticks: 1040, Timebase: Timebase48k} // gap allowed
	if err := CheckAdjacent(prevEnd, nextStart); err != nil {
		t.Fatalf("gap should be allowed: %v", err)
	}

	overlap := Timestamp{Ticks: 980, Timebase: Timebase48k}
	if err := CheckAdjacent(prevEnd, overlap); !HasCode(err, CodeTimestampNonMonotonic) {
		t.Fatalf("overlap err=%v want non-monotonic", err)
	}
}
