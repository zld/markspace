package clock

import (
	"errors"
	"math"
	"testing"
)

func TestConvertFloorKeepsRemainder(t *testing.T) {
	conv, err := NewConverter(30001)
	if err != nil {
		t.Fatalf("NewConverter: %v", err)
	}
	got, err := conv.ConvertFloor(1)
	if err != nil {
		t.Fatalf("ConvertFloor: %v", err)
	}
	// 1/30001 s = 90000/30001 ticks = 2.9999... -> floor 2, remainder 29998.
	if got.Ticks != 2 {
		t.Fatalf("ticks = %d, want 2", got.Ticks)
	}
	if !got.Truncated {
		t.Fatal("expected truncated flag")
	}
	if got.Remainder != 29998 {
		t.Fatalf("remainder = %d, want 29998", got.Remainder)
	}
	if got.SourceTimescale != 30001 {
		t.Fatalf("source timescale = %d", got.SourceTimescale)
	}
}

func TestConvertExactRefusesFraction(t *testing.T) {
	conv, err := NewConverter(30001)
	if err != nil {
		t.Fatalf("NewConverter: %v", err)
	}
	if _, err := conv.ConvertExact(1); err == nil {
		t.Fatal("expected inexact conversion to fail")
	} else {
		var inexact *ErrInexact
		if !errors.As(err, &inexact) {
			t.Fatalf("wrong error type: %T", err)
		}
	}
	conv, err = NewConverter(30000)
	if err != nil {
		t.Fatalf("NewConverter: %v", err)
	}
	tick, err := conv.ConvertExact(3000)
	if err != nil || tick != 9000 {
		t.Fatalf("exact conversion = %d, %v; want 9000", tick, err)
	}
}

func TestZeroTimescaleRejected(t *testing.T) {
	_, err := NewConverterForTrack(4, 0)
	if err == nil {
		t.Fatal("expected error for zero timescale")
	}
	var noBase *ErrNoTimeBase
	if !errors.As(err, &noBase) || noBase.TrackID != 4 {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAdderDoesNotDrift(t *testing.T) {
	// 44100 samples of one tick each is exactly one second.
	adder, err := NewAdder(44100)
	if err != nil {
		t.Fatalf("NewAdder: %v", err)
	}
	var last Tick
	for i := 0; i < 44100; i++ {
		last, _, err = adder.Add(1)
		if err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	if last != Tick(TickRate) {
		t.Fatalf("accumulated %d ticks, want %d", last, TickRate)
	}
	if adder.Remainder() != 0 {
		t.Fatalf("residual carry = %d, want 0", adder.Remainder())
	}
}

func TestAdderCarryAcrossFractionalSamples(t *testing.T) {
	adder, err := NewAdder(30001)
	if err != nil {
		t.Fatalf("NewAdder: %v", err)
	}
	first, _, err := adder.Add(1)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if first != 2 {
		t.Fatalf("first = %d, want 2", first)
	}
	second, _, err := adder.Add(1)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	// 2 source ticks at 30001 = 5.9998 shared ticks -> floor 5.
	if second != 5 {
		t.Fatalf("second = %d, want 5", second)
	}
	if adder.Remainder() != 29995 {
		t.Fatalf("carry = %d, want 29995", adder.Remainder())
	}
}

func TestAdderOverflowGuarded(t *testing.T) {
	adder, err := NewAdder(1)
	if err != nil {
		t.Fatalf("NewAdder: %v", err)
	}
	if _, _, err := adder.Add(uint64(math.MaxInt64)); err == nil {
		t.Fatal("expected overflow error")
	}
}

func TestCheckedOffsets(t *testing.T) {
	if _, _, err := Checked(0, 10, 10, "box"); err != nil {
		t.Fatalf("in-bounds rejected: %v", err)
	}
	if _, _, err := Checked(5, 6, 10, "box"); err == nil {
		t.Fatal("expected out-of-range error")
	} else {
		var oor *ErrOutOfRange
		if !errors.As(err, &oor) || oor.Offset != 5 || oor.Limit != 10 {
			t.Fatalf("wrong bounds error: %v", err)
		}
	}
	if _, _, err := Checked(-1, 1, 10, "box"); err == nil {
		t.Fatal("expected negative offset error")
	}
}

func TestOffsetErrorCarriesOriginalPosition(t *testing.T) {
	base := errors.New("bad sample magic")
	off := NewOffsetError(4096, CategoryCorruptSample, "sample 7", base)
	if got, ok := OffsetOf(off); !ok || got != 4096 {
		t.Fatalf("OffsetOf = %d, %v", got, ok)
	}
	if !errors.Is(off, base) {
		t.Fatal("wrapped error must remain unwrapable")
	}
	if _, ok := OffsetOf(base); ok {
		t.Fatal("plain error must not report an offset")
	}
}
