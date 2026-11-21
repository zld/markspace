package ts

import "testing"

func TestClockWrap(t *testing.T) {
	var c Clock
	near := Max33 - TicksPerSecond
	first := c.Push(near)
	second := c.Push(TicksPerSecond) // wrapped one second after
	if second <= first {
		t.Fatalf("expected monotonic: %d -> %d", first, second)
	}
	if got := Seconds(second - first); got < 1.99 || got > 2.01 {
		t.Fatalf("wrap delta = %v", got)
	}
	if c.Rolls() != 1 {
		t.Fatalf("rolls = %d", c.Rolls())
	}
}

func TestTimelineBackward(t *testing.T) {
	var tl Timeline
	if _, back := tl.ObservePTS(90000); back {
		t.Fatal("first sample cannot be backward")
	}
	if _, back := tl.ObservePTS(180000); back {
		t.Fatal("forward sample flagged")
	}
	// A small genuine backward jump (no wraparound) must be flagged.
	if _, back := tl.ObservePTS(100000); !back {
		t.Fatal("backward PTS not detected")
	}
}

func TestSplitAnnexB(t *testing.T) {
	stream := []byte{
		0, 0, 0, 1, 0x65, 0xAA, 0xBB, // IDR (ref_idc=3)
		0, 0, 1, 0x41, 0xCC, // non-IDR
		0, 0, 0, 1, 0x67, 0xDD, // SPS
	}
	units := SplitAnnexB(stream)
	if len(units) != 3 {
		t.Fatalf("nal count = %d", len(units))
	}
	want := []byte{NALIDR, NALNonIDR, NALSPS}
	for i := range want {
		if units[i].Type != want[i] {
			t.Fatalf("nal %d type = %d want %d", i, units[i].Type, want[i])
		}
	}
	if !ContainsIDR(stream) {
		t.Fatal("IDR not detected")
	}
	if FirstNALType(stream) != NALIDR {
		t.Fatalf("first nal = %d", FirstNALType(stream))
	}
}

func TestEmptyAnnexB(t *testing.T) {
	if len(SplitAnnexB([]byte{0, 0})) != 0 {
		t.Fatal("expected no NAL units")
	}
	if FirstNALType(nil) != -1 {
		t.Fatal("expected -1 for empty stream")
	}
}
