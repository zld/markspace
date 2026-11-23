package clock

import (
	"errors"
	"testing"

	"mediatool/internal/fault"
)

const baseNTPUnix = int64(1_700_000_000) // arbitrary but fixed

func nsToNTP(ns int64) NTP { return NsToNTP(ns) }

func TestExtendRTPHandlesWraparound(t *testing.T) {
	c := NewTrackClock(1, 100, 90000)
	const start = uint32(0xFFFFFFF0)
	if got := c.ExtendRTP(start); got != uint64(start) {
		t.Fatalf("first extension = %d, want %d", got, start)
	}
	next := start
	next += 16
	// 16 ticks later crosses 2^32 exactly once.
	got := c.ExtendRTP(next)
	want := uint64(start) + 16
	if got != want {
		t.Fatalf("post-wrap extension = %d, want %d", got, want)
	}
	// A late packet just before the wrap still maps backwards correctly.
	lateVal := start
	lateVal += 8
	late := c.ExtendRTP(lateVal)
	if late != uint64(start)+8 {
		t.Fatalf("late extension = %d, want %d", late, start+8)
	}
}

func TestExtendRTPReorderedFarFutureUsesSignedDelta(t *testing.T) {
	c := NewTrackClock(1, 1, 48000)
	c.ExtendRTP(0xFFFFFF00)
	// Timestamp 0x00000100 is 0x200 ticks ahead across the wrap.
	if got, want := c.ExtendRTP(0x00000100), uint64(0xFFFFFF00)+0x200; got != want {
		t.Fatalf("wrap-forward extension = %d, want %d", got, want)
	}
}

func TestMapWithoutSRReportsInvalidNotZero(t *testing.T) {
	c := NewTrackClock(7, 7, 48000)
	st := c.Map(48000)
	if st.Valid {
		t.Fatalf("expected invalid stamp without sender report")
	}
	if st.Reason == "" {
		t.Fatalf("expected a reason for an invalid stamp")
	}
	if _, err := c.MapAt(48000, 42); err == nil {
		t.Fatalf("expected located fault for missing sender report")
	} else {
		var f *fault.Fault
		if !errors.As(err, &f) || f.Offset != 42 || f.Kind != fault.KindRTCPInvalid {
			t.Fatalf("unexpected fault %v", err)
		}
	}
}

func TestSenderReportEstablishesMediaTime(t *testing.T) {
	tl := New()
	c := tl.Register(1, 0xabc, 48000)
	sr := SenderReport{TrackID: 1, SSRC: 0xabc, ClockRate: 48000, RTP: 1000, NTP: nsToNTP(baseNTPUnix * 1e9)}
	if err := tl.ObserveSR(sr); err != nil {
		t.Fatalf("ObserveSR: %v", err)
	}
	// Half a second later: 24000 ticks.
	st := c.Map(1000 + 24000)
	if !st.Valid || !st.Trusted {
		t.Fatalf("stamp not valid/trusted: %+v", st)
	}
	if st.MediaMs != 500 {
		t.Fatalf("media ms = %d, want 500", st.MediaMs)
	}
}

func TestRegressionMarksUntrustedIntervalAndKeepsTime(t *testing.T) {
	tl := New()
	c := tl.Register(2, 0xdef, 90000)
	t0 := baseNTPUnix * 1e9
	sr1 := SenderReport{TrackID: 2, SSRC: 0xdef, ClockRate: 90000, RTP: 100, NTP: nsToNTP(t0), Offset: 10}
	if err := tl.ObserveSR(sr1); err != nil {
		t.Fatalf("first SR: %v", err)
	}
	// A report one second in the past, and an RTP timestamp that also went back.
	sr2 := SenderReport{TrackID: 2, SSRC: 0xdef, ClockRate: 90000, RTP: 50, NTP: nsToNTP(t0 - 1e9), Offset: 900}
	err := tl.ObserveSR(sr2)
	if err == nil {
		t.Fatalf("expected regression fault")
	}
	var f *fault.Fault
	if !errors.As(err, &f) || f.Kind != fault.KindRTCPInvalid || f.Offset != 900 {
		t.Fatalf("unexpected regression fault %v", err)
	}
	if f.Recovery != fault.RecoverContinue {
		t.Fatalf("recovery = %s, want continue", f.Recovery)
	}

	// Media time must continue from the last trusted base, not reset to zero.
	st := c.Map(100 + 90000)
	if !st.Valid {
		t.Fatalf("stamp became invalid after regression")
	}
	if st.MediaMs != 1000 {
		t.Fatalf("media ms after regression = %d, want 1000", st.MediaMs)
	}

	u := c.Untrusted()
	if len(u) != 1 {
		t.Fatalf("untrusted intervals = %d, want 1", len(u))
	}
	if u[0].Reason != "rtcp_time_regression" || u[0].Offset != 900 {
		t.Fatalf("unexpected untrusted record %+v", u[0])
	}
	// Timestamps inside the untrusted span are flagged, not zeroed.
	inside := c.Map(100 + 1) // just after the first SR
	if inside.Trusted {
		t.Fatalf("expected timestamp near regression to be untrusted")
	}
	if inside.MediaMs == 0 && u[0].FromNs == 0 {
		t.Fatalf("regression silently zeroed media time")
	}
}

func TestClockRateMismatchRejected(t *testing.T) {
	c := NewTrackClock(3, 1, 48000)
	if err := c.ObserveSR(SenderReport{TrackID: 3, SSRC: 1, ClockRate: 48000, NTP: nsToNTP(1 << 40)}); err != nil {
		t.Fatalf("first SR: %v", err)
	}
	err := c.ObserveSR(SenderReport{TrackID: 3, SSRC: 1, ClockRate: 44100, NTP: nsToNTP((1 << 40) + 1e9)})
	if err == nil || !fault.IsKind(err, fault.KindRTCPInvalid) {
		t.Fatalf("expected clock-rate mismatch fault, got %v", err)
	}
}

func TestNTPRoundTrip(t *testing.T) {
	for _, ns := range []int64{0, 1, 999_999_999, 1_700_000_000_123_456_789, 3_800_000_000_000_000_000} {
		got := NTPToNs(NsToNTP(ns))
		// Accept sub-microsecond rounding from the 32-bit fraction.
		if d := got - ns; d > 1e6 || d < -1e6 {
			t.Fatalf("roundtrip %d -> %d (delta %d)", ns, got, d)
		}
	}
}

func TestUntrustedIntervalIsBounded(t *testing.T) {
	c := NewTrackClock(4, 4, 90000)
	t0 := baseNTPUnix * 1e9
	last := int64(0)
	for i := 0; i < 500; i++ {
		ntp := t0 + int64(i)*1e9
		_ = c.ObserveSR(SenderReport{TrackID: 4, SSRC: 4, ClockRate: 90000, RTP: uint32(i * 1000), NTP: nsToNTP(ntp), Offset: int64(i)})
		_ = c.ObserveSR(SenderReport{TrackID: 4, SSRC: 4, ClockRate: 90000, RTP: uint32(i*1000 - 10), NTP: nsToNTP(ntp - 5e8), Offset: int64(i)})
		last = int64(i)
	}
	if got := len(c.Untrusted()); got > maxUntrusted {
		t.Fatalf("untrusted intervals = %d, exceeds bound %d", got, maxUntrusted)
	}
	if last == 0 {
		t.Fatal("loop did not run")
	}
}
