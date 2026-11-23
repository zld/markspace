package clock

import (
	"math"
	"testing"
)

func TestSeqDistance(t *testing.T) {
	cases := []struct {
		a, b uint16
		want int32
	}{
		{10, 12, 2},
		{65535, 1, 2},
		{1, 65535, -2},
		{0, 0, 0},
	}
	for _, c := range cases {
		if got := SeqDistance(c.a, c.b); got != c.want {
			t.Fatalf("SeqDistance(%d,%d)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestUnwrapTSForwardAndWrap(t *testing.T) {
	prev := int64(math.MaxUint32 - 2)
	next, wraps, err := UnwrapTS(prev, 3)
	if err != nil {
		t.Fatal(err)
	}
	if wraps != 1 {
		t.Fatalf("wraps=%d want 1", wraps)
	}
	if want := prev + 6; next != want {
		t.Fatalf("extended=%d want %d", next, want)
	}
}

func TestUnwrapTSNoSpuriousWrap(t *testing.T) {
	prev := int64(1_000_000)
	next, wraps, err := UnwrapTS(prev, 1_000_090)
	if err != nil || wraps != 0 || next != prev+90 {
		t.Fatalf("got %d wraps=%d err=%v", next, wraps, err)
	}
}

func TestOverflowGuards(t *testing.T) {
	if _, err := Add64(math.MaxInt64, 1); err == nil || err.Kind != ErrOverflow {
		t.Fatal("Add64 overflow not detected")
	}
	if _, err := Mul64(math.MaxInt64, 3); err == nil || err.Kind != ErrOverflow {
		t.Fatal("Mul64 overflow not detected")
	}
	if err := CheckOffset(100, 50, 150); err != nil {
		t.Fatalf("valid offset rejected: %v", err)
	}
	if err := CheckOffset(100, 50, 120); err == nil || err.Kind != ErrIndexOutOfBounds {
		t.Fatal("out-of-bounds offset not detected")
	}
}

func TestErrorClassification(t *testing.T) {
	err := NewError(ErrTruncated, "sample cut short")
	got, ok := AsError(err)
	if !ok || got.Kind != ErrTruncated {
		t.Fatal("error classification round-trip failed")
	}
	if got.Error() == "" {
		t.Fatal("empty error string")
	}
}
