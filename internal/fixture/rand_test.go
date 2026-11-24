package fixture

import "testing"

func TestRandDeterministic(t *testing.T) {
	a := NewRand(7)
	b := NewRand(7)
	for i := 0; i < 1000; i++ {
		if a.Uint64() != b.Uint64() {
			t.Fatalf("same seed diverged at step %d", i)
		}
	}
	if NewRand(7).Uint64() == NewRand(8).Uint64() {
		t.Fatal("different seeds produced identical first value")
	}
}

func TestIntnRangeAndPerm(t *testing.T) {
	r := NewRand(42)
	for i := 0; i < 1000; i++ {
		v := r.Intn(13)
		if v < 0 || v >= 13 {
			t.Fatalf("Intn out of range: %d", v)
		}
	}
	p := NewRand(1).Perm(10)
	seen := make(map[int]bool)
	for _, v := range p {
		if seen[v] {
			t.Fatalf("permutation repeats %d: %v", v, p)
		}
		seen[v] = true
	}
	if len(seen) != 10 {
		t.Fatalf("permutation %v does not cover 0..9", p)
	}
}

func TestFillStable(t *testing.T) {
	r1, r2 := NewRand(99), NewRand(99)
	x, y := make([]byte, 33), make([]byte, 33)
	r1.Fill(x)
	r2.Fill(y)
	for i := range x {
		if x[i] != y[i] {
			t.Fatalf("Fill diverged at byte %d", i)
		}
	}
}
