package clock

import (
	"math"
	"testing"
)

func TestUnwrap32BitTimestamp(t *testing.T) {
	u, err := NewTimestampUnwrapper(48000)
	if err != nil {
		t.Fatal(err)
	}
	exp, reason := u.Unwrap(1000)
	if reason != ReasonInit || exp != 1000 {
		t.Fatalf("init: exp=%d reason=%s", exp, reason)
	}
	if exp, r := u.Unwrap(1960); r != ReasonNormal || exp != 1960 {
		t.Fatalf("normal: exp=%d reason=%s", exp, r)
	}
	// one frame (960 ticks) forward while crossing 2^32
	boundary := uint64(math.MaxUint32) - 200
	exp, _ = u.Unwrap(boundary)
	exp2, r := u.Unwrap((boundary + 960) % u.mod)
	if r != ReasonWrap {
		t.Fatalf("expected wrap, got %s", r)
	}
	if exp2-exp != 960 {
		t.Fatalf("wrap continuity broken: %d -> %d", exp, exp2)
	}
	if u.Wraps() != 1 {
		t.Fatalf("wrap count = %d", u.Wraps())
	}
}

func TestUnwrap16BitSequenceWithReorder(t *testing.T) {
	u, _ := NewSequenceUnwrapper()
	seqs := []uint64{10, 11, 12, 11, 65534, 65535, 0, 1}
	expanded := make([]int64, 0, len(seqs))
	for i, s := range seqs {
		exp, r := u.Unwrap(s)
		switch i {
		case 0:
			if r != ReasonInit {
				t.Fatalf("seq %d: %s", s, r)
			}
		case 3:
			if r != ReasonLate || exp != 11 {
				t.Fatalf("late reorder misclassified: exp=%d r=%s", exp, r)
			}
		case 6:
			if r != ReasonWrap {
				t.Fatalf("16-bit wrap expected, got %s", r)
			}
		default:
			if r == ReasonReset || r == ReasonJump {
				t.Fatalf("seq %d unexpected %s", s, r)
			}
		}
		expanded = append(expanded, exp)
	}
	if expanded[6]-expanded[5] != 1 || expanded[7]-expanded[6] != 1 {
		t.Fatalf("post-wrap ordering wrong: %v", expanded)
	}
}

func TestUnwrapResetAndJumpRebuildBaseline(t *testing.T) {
	u, _ := NewUnwrapper(16, 512, 30000)
	u.Unwrap(5000)
	last, _ := u.Unwrap(5100)
	// big backwards move: reset, output stays strictly monotonic
	exp, r := u.Unwrap(10)
	if r != ReasonReset || exp != last+1 {
		t.Fatalf("reset: exp=%d last=%d reason=%s", exp, last, r)
	}
	exp2, r2 := u.Unwrap(40000)
	if r2 != ReasonJump || exp2 != exp+1 {
		t.Fatalf("jump: exp2=%d exp=%d reason=%s", exp2, exp, r2)
	}
	if u.Resets() != 2 {
		t.Fatalf("reset count = %d", u.Resets())
	}
}

func TestUnwrapCheckpointRoundTrip(t *testing.T) {
	u, _ := NewTimestampUnwrapper(44100)
	for _, v := range []uint64{100, 200, math.MaxUint32 - 5, 50, 60} {
		u.Unwrap(v)
	}
	snap := u.Snapshot()
	restored, err := RestoreUnwrap(snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []uint64{70, 1000, 2000} {
		want, _ := u.Unwrap(v)
		got, _ := restored.Unwrap(v)
		if want != got {
			t.Fatalf("restored unwrapper diverges at %d: %d != %d", v, got, want)
		}
	}
}

func TestUnwrapRejectsBadConfig(t *testing.T) {
	if _, err := NewUnwrapper(4, 1, 2); err == nil {
		t.Fatal("accepted bit width out of range")
	}
	if _, err := NewUnwrapper(16, 100, 50); err == nil {
		t.Fatal("accepted inverted thresholds")
	}
}
