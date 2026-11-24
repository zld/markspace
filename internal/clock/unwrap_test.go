package clock

import "testing"

func TestUnwrap33_MonotonicWrap(t *testing.T) {
	u := NewUnwrapper33()
	nearTop := Mod33 - 5
	first := u.Feed(nearTop)
	if first.Ambiguous || first.Value != nearTop {
		t.Fatalf("first feed = %+v", first)
	}
	second := u.Feed(7) // wrapped from Mod33-5 to 7
	if !second.Wrapped || second.Ambiguous {
		t.Fatalf("wrap feed = %+v", second)
	}
	if second.Value != Mod33+7 {
		t.Fatalf("unwrapped value = %d, want %d", second.Value, Mod33+7)
	}
	if third := u.Feed(8); third.Value <= second.Value || third.Ambiguous {
		t.Fatalf("post-wrap value must advance: %+v", third)
	}
}

func TestUnwrap33_RepeatedValue(t *testing.T) {
	u := NewUnwrapper33()
	u.Feed(1000)
	r := u.Feed(1000)
	if r.Value != 1000 || r.Wrapped || r.Ambiguous {
		t.Fatalf("repeat feed = %+v", r)
	}
}

func TestUnwrap33_AmbiguousJump(t *testing.T) {
	u := NewUnwrapper33()
	u.Feed(10)
	r := u.Feed(10 + uint64(HalfMod33) + 1)
	if !r.Ambiguous {
		t.Fatalf("oversized jump must be ambiguous, got %+v", r)
	}
	// State must not advance on ambiguity; the next sane feed still works.
	ok := u.Feed(11)
	if ok.Ambiguous || ok.Value != 11 {
		t.Fatalf("recovery after ambiguity failed: %+v", ok)
	}
}

func TestUnwrap33_OutOfRange(t *testing.T) {
	u := NewUnwrapper33()
	if r := u.Feed(Mod33); !r.Ambiguous {
		t.Fatalf("value >= 2^33 must be ambiguous: %+v", r)
	}
}
