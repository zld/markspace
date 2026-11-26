package media

import (
	"errors"
	"math"
	"testing"
)

func TestParseSecondsToNSExact(t *testing.T) {
	got, err := ParseSecondsToNS("1.500")
	if err != nil {
		t.Fatal(err)
	}
	if got != int64(1500*Millisecond) {
		t.Fatalf("got %d", got)
	}
	if _, err := ParseSecondsToNS("0"); err == nil {
		t.Fatal("zero duration must fail")
	}
}

func TestDurationFromTimescaleOverflow(t *testing.T) {
	if _, err := DurationFromTimescale(math.MaxInt64, 1); err == nil {
		t.Fatal("expected overflow")
	} else {
		var f *Fault
		if !errors.As(err, &f) || f.Code != CodeTimeOverflow {
			t.Fatalf("want time overflow, got %v", err)
		}
	}
	got, err := DurationFromTimescale(45000, 90000)
	if err != nil {
		t.Fatal(err)
	}
	if got != 500_000_000 {
		t.Fatalf("got %d", got)
	}
}

func TestUnwrapModuloAndAmbiguity(t *testing.T) {
	v, err := UnwrapModulo(8, 9, 4)
	if err != nil || v != 9 {
		t.Fatalf("v=%d err=%v", v, err)
	}
	v, err = UnwrapModulo(15, 1, 4)
	if err != nil || v != 17 {
		t.Fatalf("wrap v=%d err=%v", v, err)
	}
	v, err = UnwrapModulo(-1, 2, 4)
	if err != nil || v != 2 {
		t.Fatalf("negative-side nearest v=%d err=%v", v, err)
	}
	v, err = UnwrapModulo(-1, 14, 4)
	if err != nil || v != -2 {
		t.Fatalf("negative wrap v=%d err=%v", v, err)
	}
	if _, err := UnwrapModulo(0, 8, 4); err == nil {
		t.Fatal("half-window jump must be ambiguous")
	}
}

func TestUnwrapSequence16Wrap(t *testing.T) {
	var state uint64
	if got := UnwrapSequence16(&state, 65535); got != 65535 {
		t.Fatalf("first=%d", got)
	}
	if got := UnwrapSequence16(&state, 0); got != 65536 {
		t.Fatalf("wrap=%d", got)
	}
	if got := UnwrapSequence16(&state, 2); got != 65538 {
		t.Fatalf("advance=%d", got)
	}
}
