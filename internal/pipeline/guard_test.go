package pipeline

import "testing"

func TestMemoryGuardBudget(t *testing.T) {
	g := NewMemoryGuard(3)
	for i := 0; i < 3; i++ {
		if !g.Acquire() {
			t.Fatalf("acquire %d should succeed within budget", i)
		}
	}
	if g.Acquire() {
		t.Fatal("acquire beyond budget must report back-pressure, not grow")
	}
	if g.InFlight() != 3 || g.Limit() != 3 {
		t.Fatalf("guard state wrong: in=%d limit=%d", g.InFlight(), g.Limit())
	}
	g.Release()
	if !g.Acquire() {
		t.Fatal("a released slot must be reusable")
	}
}

func TestMemoryGuardReleaseAtZero(t *testing.T) {
	g := NewMemoryGuard(2)
	g.Release() // must not go negative
	if g.InFlight() != 0 {
		t.Fatal("release without acquire must stay at zero")
	}
}
