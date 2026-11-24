package index

import (
	"testing"

	"mediasync/internal/clock"
	"mediasync/internal/rtp"
)

func sampleIntervals() ([]rtp.Interval, []rtp.Gap) {
	step := clock.Tick(900)
	iv := []rtp.Interval{
		{Index: 0, StartSeq: 0, EndSeq: 9, StartTS: 0, EndTS: 10 * step, Concealed: 3},
	}
	gaps := []rtp.Gap{
		{StartSeq: 4, EndSeq: 6, Packets: 3, StartTS: 4 * step, EndTS: 7 * step, Kind: rtp.GapConcealed},
	}
	return iv, gaps
}

func TestRebuildSortedAndBounded(t *testing.T) {
	iv, gaps := sampleIntervals()
	const blockAlign, framesPerPacket = 4, 480 // 10 ms of 48 kHz mono-ish frames
	inputLength := int64(10 * framesPerPacket * blockAlign)
	recs, err := Rebuild(iv, gaps, inputLength, blockAlign, framesPerPacket)
	if err != nil {
		t.Fatal(err)
	}
	// One entry per valid interval: sampleIntervals has a single interval.
	if len(recs) != len(iv) {
		t.Fatalf("entries=%d want %d (one per interval)", len(recs), len(iv))
	}
	for i, r := range recs {
		if r.TimeTicks != iv[i].StartTS {
			t.Fatalf("record %d timestamp=%d want %d", i, r.TimeTicks, iv[i].StartTS)
		}
		if r.ByteOffset+r.Length > inputLength {
			t.Fatalf("record %d crosses input boundary", i)
		}
		if r.ByteOffset != r.FrameIndex*int64(blockAlign) {
			t.Fatalf("record %d offset/frame mismatch", i)
		}
		if r.Length != int64(10*framesPerPacket)*blockAlign {
			t.Fatalf("record %d must span the whole interval: len=%d", i, r.Length)
		}
		if i > 0 && r.TimeTicks < recs[i-1].TimeTicks {
			t.Fatal("records not time-sorted")
		}
	}
	if !recs[0].Concealed {
		t.Fatalf("concealed flag not propagated to interval record: %+v", recs[0])
	}
}

func TestRebuildOneEntryPerInterval(t *testing.T) {
	step := clock.Tick(900)
	// Two intervals separated by a blocked gap.
	iv := []rtp.Interval{
		{Index: 0, StartSeq: 0, EndSeq: 4, StartTS: 0, EndTS: 5 * step},
		{Index: 1, StartSeq: 20, EndSeq: 24, StartTS: 20 * step, EndTS: 25 * step},
	}
	gaps := []rtp.Gap{
		{StartSeq: 5, EndSeq: 19, Packets: 15, StartTS: 5 * step, EndTS: 20 * step, Kind: rtp.GapBlocked},
	}
	const blockAlign, framesPerPacket = 4, 480
	inputLength := int64(10 * framesPerPacket * blockAlign)
	recs, err := Rebuild(iv, gaps, inputLength, blockAlign, framesPerPacket)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("entries=%d want 2 (one per interval)", len(recs))
	}
	if recs[1].ByteOffset != int64(5*framesPerPacket)*blockAlign {
		t.Fatalf("second interval must continue byte geometry: %+v", recs[1])
	}
	if recs[0].Concealed || recs[1].Concealed {
		t.Fatal("blocked-gap intervals must not be flagged concealed")
	}
}

func TestRebuildRejectsOverflowGeometry(t *testing.T) {
	iv, gaps := sampleIntervals()
	// Input far too small for the declared intervals.
	if _, err := Rebuild(iv, gaps, 100, 4, 480); err == nil {
		t.Fatal("expected overflow error for geometry beyond input length")
	}
}

func TestSanitizeDropsCorruptDuplicateOutOfRange(t *testing.T) {
	const blockAlign, inputLength = 4, 1000
	good := Record{ID: 0, TimeTicks: 10, ByteOffset: 0, FrameIndex: 0, Length: 8}
	dupTime := Record{ID: 1, TimeTicks: 10, ByteOffset: 8, FrameIndex: 2, Length: 8}
	dupOffset := Record{ID: 2, TimeTicks: 20, ByteOffset: 0, FrameIndex: 0, Length: 8}
	outOfRange := Record{ID: 3, TimeTicks: 30, ByteOffset: 996, FrameIndex: 249, Length: 8}
	corrupt := Record{ID: 4, TimeTicks: 40, ByteOffset: -5, FrameIndex: -1, Length: 8}
	badLen := Record{ID: 5, TimeTicks: 50, ByteOffset: 16, FrameIndex: 4, Length: 7} // not multiple of 4
	unsorted := Record{ID: 6, TimeTicks: 0, ByteOffset: 24, FrameIndex: 6, Length: 8}

	out, report := Sanitize([]Record{good, dupTime, dupOffset, outOfRange, corrupt, badLen, unsorted},
		inputLength, blockAlign)
	if report.OutOfRange != 1 || report.Duplicate != 2 || report.Corrupt != 2 ||
		report.TotalDropped != 5 {
		t.Fatalf("report=%+v", report)
	}
	if len(out) != 2 || out[0].TimeTicks != 0 || out[1].TimeTicks != 10 {
		t.Fatalf("salvaged records wrong or unsorted: %+v", out)
	}
	for _, r := range out {
		if r.ByteOffset+r.Length > inputLength {
			t.Fatalf("salvaged record crosses input: %+v", r)
		}
	}
}

func TestRecordValidate(t *testing.T) {
	if err := (Record{ByteOffset: 0, FrameIndex: 0, Length: 8}).Validate(16, 4); err != nil {
		t.Fatalf("valid record rejected: %v", err)
	}
	if err := (Record{ByteOffset: 12, FrameIndex: 3, Length: 8}).Validate(16, 4); err == nil {
		t.Fatal("record crossing input length must be rejected")
	}
}
