package timeline

import (
	"errors"
	"testing"

	"github.com/pion/rtp"
)

func hdr(ts uint32) *rtp.Header { return &rtp.Header{Timestamp: ts} }

func TestRTPUnwrapMonotonicAcrossWrap(t *testing.T) {
	u := NewRTPUnwrapper(90000, 0)
	seq := []uint32{0xFFFFFFF0, 0xFFFFFFF8, 0x00000004, 0x00000010}
	var prev uint64
	for i, ts := range seq {
		v, err := u.Expand(hdr(ts))
		if err != nil {
			t.Fatalf("expand %d: %v", i, err)
		}
		if i > 0 && v <= prev {
			t.Fatalf("unwrapped not monotonic at %d: %d <= %d", i, v, prev)
		}
		prev = v
	}
	// 0x10 after wrap must equal one full period + 0x10.
	want := uint64(0x100000000) + 0x10
	if prev != want {
		t.Fatalf("post-wrap value = %d, want %d", prev, want)
	}
}

func TestRTPUnwrapRejectsImpossibleJump(t *testing.T) {
	u := NewRTPUnwrapper(90000, 1000)
	if _, err := u.Expand(hdr(10)); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Expand(hdr(99999)); err == nil {
		t.Fatal("forward jump beyond maxJump must error")
	}
	// Backward movement too large to be a wrap.
	u2 := NewRTPUnwrapper(90000, 1000)
	if _, err := u2.Expand(hdr(5000)); err != nil {
		t.Fatal(err)
	}
	_, err := u2.Expand(hdr(1000))
	var me *MediaError
	if !errors.As(err, &me) || me.Code != ErrTimeline {
		t.Fatalf("want E_TIMELINE for non-wrap rollback, got %v", err)
	}
}

func TestTrackClockTfdtRollover(t *testing.T) {
	c := NewTrackClock(ClockConfig{TrackID: 1, Timescale: 90000})
	// samples just before the v0 boundary and just after
	s1 := c.MapTick(0, 0xFFFFFFE0, 16)
	s2 := c.MapTick(1, 0xFFFFFFF0, 16)
	s3 := c.MapTick(2, 0x00000000, 16)
	s4 := c.MapTick(3, 0x00000010, 16)
	for _, s := range []SampleTime{s1, s2, s3, s4} {
		if s.Invalid {
			t.Fatalf("unexpected invalid sample: %+v", s)
		}
	}
	if !(s1.EndUS <= s2.StartUS && s2.EndUS <= s3.StartUS && s3.EndUS <= s4.StartUS) {
		t.Fatalf("timeline not monotonic across rollover: %+v %+v %+v %+v", s1, s2, s3, s4)
	}
	if c.Rollovers() != 1 {
		t.Fatalf("rollovers = %d, want 1", c.Rollovers())
	}
	// One 90kHz tick period: 2^32 ticks * 1e6/90000 us.
	if s3.AdjustUS <= 0 {
		t.Fatalf("rollover adjustment not recorded: %+v", s3)
	}
}

func TestTrackClockRejectsUnexplainedRollback(t *testing.T) {
	c := NewTrackClock(ClockConfig{TrackID: 1, Timescale: 48000})
	c.MapTick(0, 1_000_000, 100)
	// A small backwards jump while both sides sit far inside the 32-bit range:
	// cannot be a rollover, must be flagged invalid.
	st := c.MapTick(1, 999_900, 100)
	if !st.Invalid || st.ErrCode != ErrTimeline {
		t.Fatalf("rollback must be marked invalid, got %+v", st)
	}
}

func TestTrackClockMonotonicUnderRounding(t *testing.T) {
	// timescale 3 (coarse) makes microsecond rounding collide for close ticks.
	c := NewTrackClock(ClockConfig{TrackID: 2, Timescale: 3})
	a := c.MapTick(0, 0, 1)
	b := c.MapTick(1, 1, 1)
	if b.StartUS < a.EndUS {
		t.Fatalf("rounded samples must stay monotonic: %+v %+v", a, b)
	}
}

func TestTrackClockUSConversion(t *testing.T) {
	c := NewTrackClock(ClockConfig{Timescale: 1_000_000})
	st := c.MapTick(0, 1_000_000, 500_000)
	if st.StartUS != 1_000_000 || st.EndUS != 1_500_000 {
		t.Fatalf("1MHz tick conversion wrong: %+v", st)
	}
}

func TestCheckDrift(t *testing.T) {
	c := NewTrackClock(ClockConfig{Timescale: 90000, DriftPPMMax: 500})
	// 1.01s media vs 1.00s reference -> 10000 ppm
	if _, err := c.CheckDrift(1_010_000, 1_000_000); err == nil {
		t.Fatal("drift over limit must error")
	}
	ppm, err := c.CheckDrift(1_000_100, 1_000_000) // 100 ppm
	if err != nil || ppm != 100 {
		t.Fatalf("within-limit drift: ppm=%d err=%v", ppm, err)
	}
}
