package rtcp_test

import (
	"testing"

	"media-check/internal/fault"
	"media-check/internal/fixture"
	"media-check/internal/rtcp"
)

func newReport(trackID uint32, startTS, rate uint32) fixture.RTCPReport {
	return fixture.RTCPReport{
		TrackID: trackID, SSRC: trackID * 10,
		NTPSeconds: 1, RTPTime: startTS, ClockRate: rate,
	}
}

func TestExactRationalMapping(t *testing.T) {
	fm := fault.NewModel()
	m := rtcp.NewTimeMapper(fm)
	// Anchor: RTP 48000 @48kHz corresponds to wall time 1s.
	if err := m.AddReport(newReport(2, 48000, 48000)); err != nil {
		t.Fatal(err)
	}
	// +2400 ticks = +50 ms -> 1.05 s exactly.
	got, ok := m.Map(2, 48000+2400)
	if !ok {
		t.Fatal("normal mapping must be unambiguous")
	}
	if got.Millis() != 1050 {
		t.Fatalf("mapped time = %s, want 1050 ms", got)
	}
	// Rational form must be exact: 21/20 s.
	if got.Num != 21 || got.Den != 20 {
		t.Fatalf("rational = %d/%d, want 21/20", got.Num, got.Den)
	}
}

func TestTimestampWrapUnwrapped(t *testing.T) {
	fm := fault.NewModel()
	m := rtcp.NewTimeMapper(fm)
	// Anchor close to the 32-bit boundary at 90 kHz.
	if err := m.AddReport(newReport(1, 0xFFFFFFF0, 90000)); err != nil {
		t.Fatal(err)
	}
	// 32 ticks later: 0xFFFFFFF0 + 32 wraps to 0x10.
	got, ok := m.Map(1, 0x10)
	if !ok {
		t.Fatal("a 32-tick wrap must stay unambiguous")
	}
	// 32 ticks / 90000 Hz = 0.355... ms after the anchor wall time (1s).
	anchor, ok := m.AnchorWall(1)
	if !ok {
		t.Fatal("anchor missing")
	}
	if got.Cmp(anchor) <= 0 {
		t.Fatalf("wrapped time %s should follow the anchor", got)
	}
	if len(fm.TimebaseFaults()) != 0 {
		t.Fatalf("ordinary wrap must not raise a time-base fault: %v", fm.TimebaseFaults())
	}
}

func TestHalfSpaceJumpIsAmbiguous(t *testing.T) {
	fm := fault.NewModel()
	m := rtcp.NewTimeMapper(fm)
	if err := m.AddReport(newReport(1, 0, 90000)); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Map(1, 0x80000000); ok {
		t.Fatal("a half-space jump must be reported as ambiguous")
	}
	if len(fm.TimebaseFaults()) == 0 {
		t.Fatal("expected a time-base fault for the unresolvable wrap")
	}
}

func TestMissingAnchorFaults(t *testing.T) {
	fm := fault.NewModel()
	m := rtcp.NewTimeMapper(fm)
	if _, ok := m.Map(99, 1234); ok {
		t.Fatal("mapping without an anchor cannot succeed")
	}
	if len(fm.ByTrack(99)) == 0 {
		t.Fatal("missing anchor must record a fault")
	}
}
