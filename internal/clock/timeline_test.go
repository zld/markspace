package clock

import (
	"testing"

	"github.com/pion/rtcp"
)

func TestExpandSequenceWraps16Bits(t *testing.T) {
	values := []uint16{65534, 65535, 0, 1, 2}
	var last uint64
	has := false
	want := []uint64{65534, 65535, 65536, 65537, 65538}
	for i, v := range values {
		last = ExpandSequence(last, has, v)
		has = true
		if last != want[i] {
			t.Fatalf("step %d: got %d want %d", i, last, want[i])
		}
	}
}

func TestExpandTimestampWraps32Bits(t *testing.T) {
	values := []uint32{0xfffffffe, 0xffffffff, 0, 160}
	var last uint64
	has := false
	want := []uint64{4294967294, 4294967295, 4294967296, 4294967456}
	for i, v := range values {
		last = ExpandTimestamp(last, has, v)
		has = true
		if last != want[i] {
			t.Fatalf("step %d: got %d want %d", i, last, want[i])
		}
	}
}

func TestTimelineUsesSenderReportAndNeverGoesBackwards(t *testing.T) {
	tl := NewTimeline(8000)
	rtpExt := tl.ExpandTimestamp(1000)
	ntp := UnixMilliToNTP(1_700_000_000_500)
	tl.ObserveSenderReport(rtpExt, &rtcp.SenderReport{NTPTime: ntp, RTPTime: 1000})

	atAnchor := tl.MediaTime(rtpExt)
	if atAnchor != 0 {
		t.Fatalf("anchor media time = %d, want 0", atAnchor)
	}
	after := tl.MediaTime(rtpExt + 160)
	if after != 20 {
		t.Fatalf("after media time = %d, want 20", after)
	}

	// A late or corrupted mapping candidate that would move backwards is clamped.
	if got := tl.MediaTime(rtpExt); got != after {
		t.Fatalf("monotonic clamp = %d, want %d", got, after)
	}
	if tl.BackwardClamps == 0 {
		t.Fatal("expected backward clamp to be recorded")
	}
}

func TestTimelineMissingReportKeepsLastMapping(t *testing.T) {
	tl := NewTimeline(8000)
	anchor := tl.ExpandTimestamp(500)
	tl.ObserveSenderReport(anchor, &rtcp.SenderReport{NTPTime: UnixMilliToNTP(2000)})
	before := tl.MediaTime(anchor + 320)
	tl.ObserveSenderReport(anchor, nil)
	afterMissing := tl.MediaTime(anchor + 480)
	if afterMissing <= before {
		t.Fatalf("time did not advance after stale report: before=%d after=%d", before, afterMissing)
	}
	if tl.StaleReports == 0 || !tl.hasMapping {
		t.Fatalf("stale=%d hasMapping=%v", tl.StaleReports, tl.hasMapping)
	}
}

func TestNTPConversionRoundTrip(t *testing.T) {
	for _, ms := range []int64{0, 1, 20, 999, 1_700_000_000_123} {
		got := NTPToUnixMilli(UnixMilliToNTP(ms))
		diff := got - ms
		if diff < 0 {
			diff = -diff
		}
		if diff > 1 {
			t.Fatalf("NTP round trip for %d produced %d (diff %d)", ms, got, diff)
		}
	}
}
