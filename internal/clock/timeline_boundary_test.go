package clock_test

import (
	"math"
	"testing"
	"time"

	"medpipe/internal/clock"
)

func TestTimelineReordersAcrossTimestampWrap(t *testing.T) {
	tl := clock.NewTimeline(48000)
	const ssrc = 42
	const step = uint32(960)
	beforeWrap := uint32(math.MaxUint32) - step
	earlier := beforeWrap - step
	afterWrap := beforeWrap + step // wraps to a small uint32 value

	// An earlier pre-wrap packet establishes the high cycle.
	extEarlier := tl.ExtendTimestamp(ssrc, earlier)
	extAfter := tl.ExtendTimestamp(ssrc, afterWrap)
	extBeforeLate := tl.ExtendTimestamp(ssrc, beforeWrap)

	if !(extEarlier < extBeforeLate && extBeforeLate < extAfter) {
		t.Fatalf("wrap order wrong: earlier=%d before=%d after=%d", extEarlier, extBeforeLate, extAfter)
	}
	if extBeforeLate-extEarlier != uint64(step) || extAfter-extBeforeLate != uint64(step) {
		t.Fatalf("wrap deltas not one frame: %d %d", extBeforeLate-extEarlier, extAfter-extBeforeLate)
	}

	first := tl.Observe(ssrc, 10, earlier, time.Unix(100, 0))
	after := tl.Observe(ssrc, 12, afterWrap, time.Unix(100, 40_000_000))
	late := tl.Observe(ssrc, 11, beforeWrap, time.Unix(100, 20_000_000))
	if !(first.MediaTime < late.MediaTime && late.MediaTime < after.MediaTime) {
		t.Fatalf("media times not monotonic: %v %v %v", first.MediaTime, late.MediaTime, after.MediaTime)
	}
	if delta := late.MediaTime - first.MediaTime; delta != 20*time.Millisecond {
		t.Fatalf("late boundary delta=%v, want 20ms", delta)
	}
}

func TestMissingRTCPReportIsLocatedFallback(t *testing.T) {
	tl := clock.NewTimeline(48000)
	err := tl.RequireSenderReport(7, 123, 4097)
	if err == nil {
		t.Fatal("expected missing RTCP report error")
	}
	if err.Code != clock.CodeMissingClockReport {
		t.Fatalf("code=%s want %s", err.Code, clock.CodeMissingClockReport)
	}
	if err.Location == nil || err.Location.Sequence != 123 || err.Location.ByteOffset != 4097 {
		t.Fatalf("error location not locatable: %+v", err.Location)
	}
	if got := tl.ClockSource(7); got != "receive_fallback" {
		t.Fatalf("clock source=%q want receive_fallback", got)
	}

	now := time.Unix(1700000000, 0).UTC()
	if applyErr := tl.ApplySenderReport(clock.SenderReport{
		SSRC: 7, NTPTime: now, RTPTime: 999,
	}, now); applyErr != nil {
		t.Fatalf("apply RTCP report: %v", applyErr)
	}
	if err := tl.RequireSenderReport(7, 124, 4200); err != nil {
		t.Fatalf("present RTCP report should not be reported missing: %v", err)
	}
	if got := tl.ClockSource(7); got != "sender_report" {
		t.Fatalf("clock source=%q want sender_report", got)
	}
}
