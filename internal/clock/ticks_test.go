package clock

import (
	"math"
	"testing"

	"mediamerge/internal/fault"
)

func TestFromSecondsAndFraction(t *testing.T) {
	got, err := FromSeconds(3)
	if err != nil || int64(got) != 270000 {
		t.Fatalf("FromSeconds(3) = %d, %v", got, err)
	}
	got, err = FromFraction(1, 1001)
	if err != nil || int64(got) != 89 { // floor(90000/1001)
		t.Fatalf("FromFraction(1,1001) = %d, %v", got, err)
	}
	if _, err := FromFraction(1, 0); err == nil {
		t.Fatal("zero denominator must error")
	}
}

func TestAddSubOverflow(t *testing.T) {
	big := Ticks(math.MaxInt64)
	if _, err := big.Add(1); err == nil || fault.As(err).Kind != fault.KindOverflow {
		t.Fatalf("max+1 must overflow, got %v", err)
	}
	one := Ticks(1)
	if _, err := one.Sub(2); err == nil || fault.As(err).Kind != fault.KindOverflow {
		t.Fatalf("negative subtraction must be rejected, got %v", err)
	}
	a, err := Ticks(90000).Sub(45000)
	if err != nil || a != 45000 {
		t.Fatalf("Sub normal case failed: %d %v", a, err)
	}
}

func TestScale(t *testing.T) {
	got, err := Ticks(1001).Scale(30000, 1001)
	if err != nil || got != 30000 {
		t.Fatalf("Scale = %d, %v", got, err)
	}
	if _, err := Ticks(5).Scale(1, 0); err == nil {
		t.Fatal("Scale by zero denominator must error")
	}
	if _, err := Ticks(math.MaxInt64).Scale(2, 1); err == nil {
		t.Fatal("Scale overflow must be detected")
	}
}

func TestCheckMonotonic(t *testing.T) {
	if err := CheckMonotonic("r", Ticks(10), Ticks(10)); err != nil {
		t.Fatal("equal timestamps are monotonic")
	}
	err := CheckMonotonic("r", Ticks(100), Ticks(99))
	if err == nil || fault.As(err).Kind != fault.KindWrap {
		t.Fatalf("backwards jump must be a wrap fault, got %v", err)
	}
}
