package clock

import (
	"testing"

	"github.com/pion/rtp"
)

func TestTimebaseConversion(t *testing.T) {
	tb, err := NewTimebase(24000)
	if err != nil {
		t.Fatal(err)
	}
	ms, err := tb.TicksToMS(48000)
	if err != nil || ms != 2000 {
		t.Fatalf("TicksToMS = %d, %v", ms, err)
	}
	ticks, err := tb.MStoTicks(1500)
	if err != nil || ticks != 36000 {
		t.Fatalf("MStoTicks = %d, %v", ticks, err)
	}
	if _, err := NewTimebase(0); err == nil {
		t.Fatal("expected zero timebase error")
	}
}

func TestRTPConversionAndWrap(t *testing.T) {
	mediaTicks, err := ConvertRTP(90000, 90000, 1000)
	if err != nil || mediaTicks != 1000 {
		t.Fatalf("ConvertRTP = %d, %v", mediaTicks, err)
	}
	ms, err := RTPHeaderToMS(rtp.Header{Timestamp: 90000}, 90000)
	if err != nil || ms != 1000 {
		t.Fatalf("RTPHeaderToMS = %d, %v", ms, err)
	}
	b, err := CheckRTPBoundary(0xfffffff0, 16, 8)
	if err != nil || !b.Wrapped || b.Delta != 32 {
		t.Fatalf("wrap boundary = %+v, %v", b, err)
	}
	if _, err := CheckRTPBoundary(100, 90, 20); err == nil {
		t.Fatal("expected backward timestamp rejection")
	}
	if b, err := CheckRTPBoundary(100, 90, 5); err != nil || !b.Wrapped {
		t.Fatalf("large modular step should wrap: %+v, %v", b, err)
	}
}

func TestEditMappingAndBounds(t *testing.T) {
	tl := Timeline{
		TrackID: 1, TrackNumber: 1, MediaTimescale: 1000, MovieTimescale: 1000,
		MediaDuration: 5000, FirstSampleTick: 250,
		Edits: []Edit{{Duration: 2000, MediaTime: 1000, MediaRateInt: 1}},
	}
	ms, idx, ok, err := tl.PresentationMS(2000)
	if err != nil || !ok || idx != 0 || ms != 1000 {
		t.Fatalf("PresentationMS = %d %d %v %v", ms, idx, ok, err)
	}
	if got, err := tl.NormalizedMS(1250); err != nil || got != 1000 {
		t.Fatalf("NormalizedMS = %d, %v", got, err)
	}

	bad := tl
	bad.Edits[0].MediaTime = 4500
	intervals := bad.ValidateEdits()
	if len(intervals) != 1 || !intervals[0].Reject || intervals[0].StartMS != 4500 {
		t.Fatalf("unexpected intervals: %+v", intervals)
	}
}
