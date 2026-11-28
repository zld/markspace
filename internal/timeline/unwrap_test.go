package timeline

import (
	"testing"

	"medcheck/internal/model"
)

func TestNoWrapAbsolute(t *testing.T) {
	u := NewUnwrapper(Boundary{Track: 1, Source: 1, Modulus: 0})
	for _, want := range []int64{100, 250, 999} {
		got, err := u.Unwrap(uint64(want))
		if err != nil || got != want {
			t.Fatalf("Unwrap(%d) = %d,%v", want, got, err)
		}
	}
}

func TestForwardWrap(t *testing.T) {
	// 16-bit style modulus.
	u := NewUnwrapper(Boundary{Track: 1, Source: 1, Modulus: 1 << 16})
	seq := []uint64{65530, 65533, 2, 5}
	want := []int64{65530, 65533, 65538, 65541}
	for i, raw := range seq {
		got, err := u.Unwrap(raw)
		if err != nil {
			t.Fatalf("step %d err %v", i, err)
		}
		if got != want[i] {
			t.Fatalf("step %d got %d want %d", i, got, want[i])
		}
	}
	if u.Rolls() != 1 {
		t.Fatalf("rolls = %d want 1", u.Rolls())
	}
}

func TestMultipleRolls(t *testing.T) {
	u := NewUnwrapper(Boundary{Modulus: 100})
	vals := []uint64{90, 95, 10, 20, 5}
	abs := []int64{90, 95, 110, 120, 205}
	for i, v := range vals {
		got, err := u.Unwrap(v)
		if err != nil || got != abs[i] {
			t.Fatalf("step %d got %d want %d err=%v", i, got, abs[i], err)
		}
	}
	if u.Rolls() != 2 {
		t.Fatalf("rolls = %d want 2", u.Rolls())
	}
}

func TestAmbiguousHalfModulusRejected(t *testing.T) {
	u := NewUnwrapper(Boundary{Modulus: 100})
	if _, err := u.Unwrap(10); err != nil {
		t.Fatal(err)
	}
	// Jump of exactly m/2 is equally forward and backward.
	_, err := u.Unwrap(60)
	ce := model.AsCodeError(err)
	if ce == nil || ce.Kind != model.KindTimeBase {
		t.Fatalf("expected time base error, got %v", err)
	}
}

func TestRawOutsideModulusRejected(t *testing.T) {
	u := NewUnwrapper(Boundary{Modulus: 100})
	if _, err := u.Unwrap(100); err == nil {
		t.Fatal("raw >= modulus must be rejected")
	}
}

func TestEqualTickStaysStable(t *testing.T) {
	u := NewUnwrapper(Boundary{Modulus: 100})
	first, _ := u.Unwrap(50)
	second, err := u.Unwrap(50)
	if err != nil || second != first {
		t.Fatalf("equal tick moved: %d -> %d err=%v", first, second, err)
	}
}

func TestSameBoundary(t *testing.T) {
	b := Boundary{Track: 7, Source: 3}
	if !SameBoundary(b, 7, 3) || SameBoundary(b, 7, 4) || SameBoundary(b, 8, 3) {
		t.Fatal("boundary matching is wrong")
	}
}
