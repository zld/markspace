package clock

import "testing"

func TestConvertRoundTrip(t *testing.T) {
	// 1 second at 48000 Hz == 1 second at 90000 Hz.
	if got := Convert(48000, 48000, UnifiedTimescale); got != 90000 {
		t.Fatalf("Convert 48000@48k = %d, want 90000", got)
	}
	// Rounding: 1 tick at 44100 -> 2.04... -> 2 ticks at 90000.
	if got := Convert(1, 44100, UnifiedTimescale); got != 2 {
		t.Fatalf("Convert 1@44100 = %d, want 2", got)
	}
	if got := Convert(12345, 90000, 90000); got != 12345 {
		t.Fatalf("identity convert = %d", got)
	}
	if got := Convert(100, 0, 90000); got != 0 {
		t.Fatalf("zero scale = %d, want 0", got)
	}
}

func TestConvertSigned(t *testing.T) {
	if got := ConvertSigned(-48000, 48000, 90000); got != -90000 {
		t.Fatalf("ConvertSigned = %d, want -90000", got)
	}
}

func TestExtendRTPWrap(t *testing.T) {
	prev := int64(-1)
	seq := []uint32{0xfffffffe, 0xffffffff, 0, 1, 2}
	want := []int64{4294967294, 4294967295, 4294967296, 4294967297, 4294967298}
	for i, raw := range seq {
		prev = ExtendRTP(prev, raw)
		if prev != want[i] {
			t.Fatalf("step %d: got %d, want %d", i, prev, want[i])
		}
	}
}

func TestExtendRTPSeqWrap(t *testing.T) {
	prev := int64(-1)
	for _, raw := range []uint16{65534, 65535, 0, 1} {
		prev = ExtendRTPSeq(prev, raw)
	}
	if prev != 65537 {
		t.Fatalf("extended seq = %d, want 65537", prev)
	}
}

func TestExtendRejectsReordering(t *testing.T) {
	// A far-out-of-order packet maps to a negative delta, not a new cycle.
	prev := ExtendRTP(-1, 100)
	got := ExtendRTP(prev, 50)
	if got != 50 {
		t.Fatalf("out-of-order extend = %d, want 50", got)
	}
}

func TestCalibrate(t *testing.T) {
	rep := Calibrate("t1", 90000, 94500, 10000)
	if !rep.WithinLimit || rep.Offset != -4500 || rep.DevAfter != 0 || rep.DevBefore != 4500 {
		t.Fatalf("unexpected report: %+v", rep)
	}
	rep = Calibrate("t2", 0, 200000, 10000)
	if rep.WithinLimit || rep.Offset != 0 || rep.DevAfter != 200000 {
		t.Fatalf("over-threshold report: %+v", rep)
	}
}
