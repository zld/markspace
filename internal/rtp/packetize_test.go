package rtp

import (
	"testing"

	"mediacore/internal/clock"
)

func TestPacketizeRoundTrip(t *testing.T) {
	payload := make([]byte, 100)
	for i := range payload {
		payload[i] = byte(i)
	}
	pkts, err := Packetize(payload, 30, 1000, 9000, 7, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkts) != 4 {
		t.Fatalf("packets = %d want 4", len(pkts))
	}
	r, _ := NewRecoverer(DefaultConfig())
	for i, raw := range pkts {
		if got := IsKeyframePacket(raw); got != (i == 0) {
			t.Fatalf("packet %d keyframe = %v", i, got)
		}
		if _, err := r.Push(raw, clock.Time(i)); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := r.Flush()
	if err != nil {
		t.Fatal(err)
	}
	if string(rep.Bytes()) != string(payload) {
		t.Fatal("recovered payload differs")
	}
}

func TestPacketizeRejectsZeroSize(t *testing.T) {
	if _, err := Packetize([]byte{1}, 0, 0, 0, 0, false, false); err != ErrZeroPayloadSize {
		t.Fatalf("err = %v", err)
	}
}

func TestIsKeyframeMalformed(t *testing.T) {
	if IsKeyframePacket([]byte{0xFF}) {
		t.Fatal("malformed packet must not be a keyframe")
	}
}

func TestStatsString(t *testing.T) {
	got := Stats{Received: 5, Unique: 3, Duplicates: 2, Recovered: 1}.String()
	want := "received=5 unique=3 dup=2 reordered=0 recovered=1 missing=0"
	if got != want {
		t.Fatalf("stats = %q", got)
	}
}
