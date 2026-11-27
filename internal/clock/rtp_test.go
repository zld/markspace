package clock

import (
	"errors"
	"testing"

	"github.com/pion/rtp"
)

func TestRTPClockUnwraps32Bit(t *testing.T) {
	// Anchor just below the 32-bit boundary, then step across it with a small,
	// normal frame delta; the raw value appears to jump backwards.
	pre := uint32(0xFFFF_FFF0)
	c, err := NewRTPClock(90000, pre, Zero())
	if err != nil {
		t.Fatal(err)
	}
	ti, err := c.Advance(pre)
	if err != nil {
		t.Fatal(err)
	}
	// The first stamp is anchored to the programme anchor (zero).
	if ti.Cmp(Zero()) != 0 {
		t.Fatalf("pre-wrap time %s", ti)
	}
	ti2, err := c.Advance(0x0000_0010) // +32 ticks across the wrap
	if err != nil {
		t.Fatal(err)
	}
	want, err := Make(32, 90000)
	if err != nil {
		t.Fatal(err)
	}
	if ti2.Cmp(want) != 0 {
		t.Fatalf("post-wrap time %s want %s", ti2, want)
	}
}

func TestRTPClockReorderDoesNotRewind(t *testing.T) {
	c, err := NewRTPClock(90000, 0, Zero())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Advance(9000); err != nil {
		t.Fatal(err)
	}
	// Late packet 100 ticks behind: mapped behind the high-water mark but the
	// clock itself must not move.
	late, err := c.Advance(8900)
	if err != nil {
		t.Fatal(err)
	}
	if late.Cmp(MustMake(8900, 90000)) != 0 {
		t.Fatalf("late packet mapped to %s", late)
	}
	next, err := c.Advance(9100)
	if err != nil {
		t.Fatal(err)
	}
	if next.Cmp(MustMake(9100, 90000)) != 0 {
		t.Fatalf("high-water mark moved: %s", next)
	}
}

func TestRTPClockRejectsZeroRate(t *testing.T) {
	if _, err := NewRTPClock(0, 0, Zero()); !errors.Is(err, ErrOverflow) {
		t.Fatalf("got %v", err)
	}
}

func TestPlanSwitchGapAndOverlap(t *testing.T) {
	step := MustMake(1, 25)
	out, err := NewRTPClock(90000, 0, Zero())
	if err != nil {
		t.Fatal(err)
	}
	// Outgoing ends at programme t=1s (90000 ticks).
	in, err := NewRTPClock(90000, 500000, Zero())
	if err != nil {
		t.Fatal(err)
	}
	// Backup stamps itself at t=6s: boundary is 1s+1/25s, so the gap is
	// 124/25 s.
	plan, err := PlanSwitch(out, 90000, in, 540000, step)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Discontinuous || plan.Forward.Cmp(MustMake(124, 25)) != 0 {
		t.Fatalf("gap plan wrong: %+v", plan)
	}
	// Offset pulls the backup back; applying it to the first incoming stamp
	// must land exactly on the boundary.
	shift, err := MakeShift(plan.Offset.N, plan.Offset.D)
	if err != nil {
		t.Fatal(err)
	}
	aligned, err := shift.Apply(plan.IncomingStart)
	if err != nil {
		t.Fatal(err)
	}
	if aligned.Cmp(plan.Boundary) != 0 {
		t.Fatalf("aligned %s != boundary %s", aligned, plan.Boundary)
	}
}

func TestPlanSwitchExactContinuity(t *testing.T) {
	step := MustMake(1, 25)
	out, _ := NewRTPClock(90000, 0, Zero())
	in, _ := NewRTPClock(90000, 0, Zero())
	plan, err := PlanSwitch(out, 90000, in, 93600, step) // one frame later
	if err != nil {
		t.Fatal(err)
	}
	if plan.Discontinuous {
		t.Fatalf("expected continuous plan, got %+v", plan)
	}
}

func TestRTPHeaderRoundTrip(t *testing.T) {
	h := &rtp.Header{
		Version:        2,
		PayloadType:    96,
		SequenceNumber: 42,
		Timestamp:      123456,
		SSRC:           777,
		Marker:         true,
	}
	raw, err := MarshalRTPHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseRTPHeader(raw)
	if err != nil {
		t.Fatal(err)
	}
	if back.Timestamp != 123456 || back.SSRC != 777 || !back.Marker {
		t.Fatalf("round trip mismatch: %+v", back)
	}
	if _, err := ParseRTPHeader([]byte{0x00}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("garbage packet: %v", err)
	}
}
