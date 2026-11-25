package clock

import "testing"

func TestRTPPacketRoundTrip(t *testing.T) {
	tb := NewTimeBase(40_000_000)
	payload := []byte{0xde, 0xad, 0xbe, 0xef}
	wire, err := tb.MarshalRTP(7, payload)
	if err != nil {
		t.Fatalf("MarshalRTP: %v", err)
	}
	seq, ts, got, err := UnmarshalRTP(wire)
	if err != nil {
		t.Fatalf("UnmarshalRTP: %v", err)
	}
	if seq != 7 {
		t.Fatalf("sequence number = %d, want 7", seq)
	}
	// Frame 7 at 25 fps starts at 280 ms; 90 kHz ticks = 25200.
	if ts != 25200 {
		t.Fatalf("timestamp = %d, want 25200", ts)
	}
	if string(got) != string(payload) {
		t.Fatalf("payload mismatch: %x", got)
	}
}

func TestRTPPacketSequenceWraps(t *testing.T) {
	tb := NewTimeBase(40_000_000)
	wire, err := tb.MarshalRTP(65536, []byte{1})
	if err != nil {
		t.Fatalf("MarshalRTP: %v", err)
	}
	seq, _, _, err := UnmarshalRTP(wire)
	if err != nil {
		t.Fatalf("UnmarshalRTP: %v", err)
	}
	if seq != 0 {
		t.Fatalf("uint16 wrap = %d, want 0", seq)
	}
}
