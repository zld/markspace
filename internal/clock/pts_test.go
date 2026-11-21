package clock

import "testing"

func TestUnwrapperNormal(t *testing.T) {
	var u Unwrapper
	exp, kind := u.Feed(0)
	if exp != 0 || kind != JumpNone {
		t.Fatalf("first feed: got %d/%v", exp, kind)
	}
	exp, kind = u.Feed(3000)
	if exp != 3000 || kind != JumpNormal {
		t.Fatalf("normal step: got %d/%v", exp, kind)
	}
}

func TestUnwrapperWrap(t *testing.T) {
	var u Unwrapper
	// Just below the modulus, then a small value: a legal wrap.
	u.Feed(Mod - 100)
	exp, kind := u.Feed(200)
	if kind != JumpWrap {
		t.Fatalf("expected wrap, got %v", kind)
	}
	if want := Mod + 200; exp != want {
		t.Fatalf("expanded: got %d, want %d", exp, want)
	}
	if u.Wraps() != 1 {
		t.Fatalf("wrap count: %d", u.Wraps())
	}
	// Monotonic line keeps growing across a second wrap.
	u.Feed(Mod - 50)
	exp, kind = u.Feed(10)
	if kind != JumpWrap || exp != 2*Mod+10 {
		t.Fatalf("second wrap: exp=%d kind=%v", exp, kind)
	}
}

func TestUnwrapperRestart(t *testing.T) {
	var u Unwrapper
	u.Feed(5_000_000)
	exp, kind := u.Feed(1_000_000)
	if kind != JumpRestart {
		t.Fatalf("expected restart, got %v", kind)
	}
	if exp != 1_000_000 {
		t.Fatalf("restart should re-base, got %d", exp)
	}
	if u.Restarts() != 1 {
		t.Fatalf("restart count: %d", u.Restarts())
	}
}

func TestUnwrapperMasks33Bits(t *testing.T) {
	var u Unwrapper
	// Bits above bit 32 must be ignored.
	exp, _ := u.Feed(Mod | 42)
	if exp != 42 {
		t.Fatalf("mask: got %d", exp)
	}
}

func TestDuration(t *testing.T) {
	if got := Duration(TicksPerSecond); got.Milliseconds() != 1000 {
		t.Fatalf("one second: %v", got)
	}
	if got := Duration(3000); got.Milliseconds() != 33 {
		t.Fatalf("one video frame: %v", got)
	}
	if got := Milliseconds(270_000); got != 3000 {
		t.Fatalf("3s ticks: %d", got)
	}
}
