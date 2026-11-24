package clock_test

import (
	"testing"
	"time"

	"medpipe/internal/clock"
)

func TestClockReportRollbackIsLocatedAndKeepsPriorCorrection(t *testing.T) {
	tl := clock.NewTimeline(48000)
	t0 := time.Unix(1_700_000_000, 0).UTC()
	if err := tl.ApplySenderReport(clock.SenderReport{SSRC: 9, NTPTime: t0, RTPTime: 1000}, t0); err != nil {
		t.Fatal(err)
	}

	rollback := tl.ApplySenderReport(clock.SenderReport{
		SSRC: 9, NTPTime: t0.Add(-time.Minute), RTPTime: 900, SourceOffset: 77,
	}, t0.Add(time.Second))
	if rollback == nil || rollback.Code != clock.CodeClockRollback {
		t.Fatalf("rollback error=%v want %s", rollback, clock.CodeClockRollback)
	}
	if rollback.Location == nil || rollback.Location.SSRC != 9 || rollback.Location.ByteOffset != 77 {
		t.Fatalf("rollback not locatable: %+v", rollback.Location)
	}

	p := tl.Observe(9, 1, 1000, t0.Add(2*time.Second))
	if !p.WallTime.Equal(t0) {
		t.Fatalf("prior correction replaced: wall=%v want %v", p.WallTime, t0)
	}
	if p.ClockSource != "sender_report" {
		t.Fatalf("source=%q want sender_report", p.ClockSource)
	}
}
