package fixture

import (
	"testing"

	"github.com/pion/rtp"
)

func TestHealthyStructure(t *testing.T) {
	s := Healthy()
	if len(s.Data) == 0 {
		t.Fatal("empty healthy sample")
	}
	if s.KeyframeEvery <= 0 {
		t.Fatal("keyframe cadence must be positive")
	}
}

func TestFixturesStableAcrossCalls(t *testing.T) {
	a := Healthy().Data
	b := Healthy().Data
	if string(a) != string(b) {
		t.Fatal("healthy sample is not deterministic")
	}
	if string(TruncatedBlock().Data) == string(a) {
		t.Fatal("truncated sample should differ from healthy")
	}
}

func TestClientLogRoundTrip(t *testing.T) {
	events := DefaultEvents()
	parsed, err := ParseClientLog(ClientLog(events))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != len(events) {
		t.Fatalf("got %d events, want %d", len(parsed), len(events))
	}
	for i := range events {
		if parsed[i].TimeMS != events[i].TimeMS || parsed[i].Kind != events[i].Kind {
			t.Errorf("event %d mismatch: %+v vs %+v", i, parsed[i], events[i])
		}
	}
}

func TestClientLogMalformed(t *testing.T) {
	if _, err := ParseClientLog([]byte("not-a-time\tevent\n")); err == nil {
		t.Fatal("malformed log must fail")
	}
}

func TestRTPStreamRoundTrip(t *testing.T) {
	stream := DefaultRTPStream()
	blobs, err := stream.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if len(blobs) != len(stream.Packets) {
		t.Fatalf("packet count %d != %d", len(blobs), len(stream.Packets))
	}
	var p rtp.Packet
	if err := p.Unmarshal(blobs[4]); err != nil {
		t.Fatal(err)
	}
	if p.SequenceNumber != stream.Packets[4].SequenceNumber ||
		p.Timestamp != stream.Packets[4].Timestamp {
		t.Errorf("round-trip mismatch: %+v", p.Header)
	}
	// 3600 ticks at 90 kHz is 40 ms.
	if ns := stream.TimestampNS(3600); ns != 40_000_000 {
		t.Errorf("timestamp ns = %d, want 40ms", ns)
	}
}

func TestMP4Structure(t *testing.T) {
	s := DefaultMP4()
	brand, tracks, ts, err := ProbeMP4(s.Data)
	if err != nil {
		t.Fatal(err)
	}
	if brand != "isom" || tracks != 1 || ts != 1000 {
		t.Errorf("probe: brand=%q tracks=%d ts=%d", brand, tracks, ts)
	}
	if err := ValidateStructure(s.Data); err != nil {
		t.Fatalf("valid MP4 rejected: %v", err)
	}
	if err := ValidateStructure(s.Data[:150]); err == nil {
		t.Fatal("truncated MP4 must be rejected")
	}
}
