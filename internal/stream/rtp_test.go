package stream

import (
	"math"
	"testing"

	"mediascan/internal/fixture"
)

func TestParseRTPBasicAndWrap(t *testing.T) {
	// 90 kHz clock, 3000 ticks/frame (~33.3 ms), force a 2^32 crossing.
	deltas := []uint32{3000, 3000, 3000}
	start := uint32(0xFFFFFFF0)
	packets, err := fixture.RTPStream(90000, start, deltas, 1)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ParseRTP(packets, 90000)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Packets) != 4 {
		t.Fatalf("packet count = %d, want 4", len(res.Packets))
	}
	// Third packet crosses the 32-bit boundary.
	if res.Packets[2].ExpandedTS <= res.Packets[1].ExpandedTS {
		t.Fatal("expanded timestamps must stay monotonic across the wrap")
	}
	if len(res.Wraps) != 1 {
		t.Fatalf("wrap events = %d, want 1", len(res.Wraps))
	}
	if len(res.NegativeJumps) != 0 {
		t.Fatalf("rollover must not be flagged as a jump: %+v", res.NegativeJumps)
	}
	// First packet anchors time zero.
	if !res.Packets[0].Time.IsZero() {
		t.Fatal("origin packet must map to time zero")
	}
	// Rational seconds: 3000/90000 = 1/30.
	if got := res.Packets[1].Time.Seconds(); math.Abs(got-1.0/30.0) > 1e-9 {
		t.Fatalf("packet time = %v", got)
	}
}

func TestParseRTPVFRIntervals(t *testing.T) {
	// Unequal tick deltas => variable frame rate; intervals must reflect
	// the actual samples rather than any fixed-cadence guess.
	deltas := []uint32{1500, 4500, 3000}
	packets, err := fixture.RTPStream(90000, 0, deltas, 0)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ParseRTP(packets, 90000)
	if err != nil {
		t.Fatal(err)
	}
	want := []float64{1500.0 / 90000, 4500.0 / 90000, 3000.0 / 90000}
	for i, w := range want {
		if math.Abs(res.FrameIntervals[i]-w) > 1e-12 {
			t.Fatalf("interval %d = %v, want %v", i, res.FrameIntervals[i], w)
		}
	}
	// Mean rate from measured span (9000 ticks = 0.1 s, 4 frames).
	if math.Abs(res.MeanFrameRate-40.0) > 1e-9 {
		t.Fatalf("mean frame rate = %v, want 40", res.MeanFrameRate)
	}
}

func TestParseRTPNegativeJumpRejected(t *testing.T) {
	first, err := fixture.RTPPacket(fixture.RTPPacketSpec{
		Sequence: 0, Timestamp: 1_000_000, PayloadLen: 8})
	if err != nil {
		t.Fatal(err)
	}
	// Jump backwards by far more than half a cycle: cannot be a wrap.
	second, err := fixture.RTPPacket(fixture.RTPPacketSpec{
		Sequence: 1, Timestamp: 1000, PayloadLen: 8})
	if err != nil {
		t.Fatal(err)
	}
	res, err := ParseRTP([][]byte{first, second}, 90000)
	if err != nil {
		t.Fatalf("parser should retain the stream and record a fault: %v", err)
	}
	if len(res.NegativeJumps) != 1 {
		t.Fatalf("negative jumps = %d, want 1", len(res.NegativeJumps))
	}
	// Monotonic line preserved: second packet did not become a new origin.
	if res.Packets[1].ExpandedTS < res.Packets[0].ExpandedTS {
		t.Fatal("negative jump must not move the timeline backwards")
	}
	if len(res.WrapFaults()) != 0 {
		t.Fatal("a backward jump must not be recorded as a wrap")
	}
}

func TestParseRTPRejectsZeroClockRate(t *testing.T) {
	if _, err := ParseRTP(nil, 0); err == nil {
		t.Fatal("zero clock rate must error")
	}
}
