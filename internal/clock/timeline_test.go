package clock

import "testing"

func TestTimebaseConversionAndPreservation(t *testing.T) {
	tb, err := NewTimebase(30_000)
	if err != nil {
		t.Fatal(err)
	}
	if got := tb.ToShared(6_000); got != 18_000 {
		t.Fatalf("ToShared(6000) = %d, want 18000", got)
	}
	if got := tb.ToSource(18_000); got != 6_000 {
		t.Fatalf("ToSource(18000) = %d, want 6000", got)
	}

	tl := Timeline{ContainerStart: 18_000}
	if err := tl.ValidateZeroPreservation(); err == nil {
		t.Fatal("expected silently-zeroed non-zero start to fail")
	}
	tl.NonZeroStartPreserved = true
	if err := tl.ValidateZeroPreservation(); err != nil {
		t.Fatalf("preserved start should pass: %v", err)
	}
}

func TestMaxDiff(t *testing.T) {
	if got := MaxDiff(120, 121, 119); got != 2 {
		t.Fatalf("MaxDiff = %d, want 2", got)
	}
}
