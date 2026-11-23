package adapter

import (
	"bytes"
	"strings"
	"testing"

	"mediad/timeline"
)

func rtpA() RTPAdapter { return RTPAdapter{} }

func TestRTPPacketRoundTrip(t *testing.T) {
	w := NewRTPWriter()
	pkts := []struct {
		seq uint16
		ts  uint32
	}{{0, 0}, {1, 3600}, {2, 7200}}
	for _, p := range pkts {
		if err := w.WritePacket(NewRTPPacket(42, p.seq, p.ts, p.seq == 0, 32)); err != nil {
			t.Fatal(err)
		}
	}
	res, err := rtpA().Parse(bytes.NewReader(w.Bytes()), Options{Timescale: 90000})
	if err != nil {
		t.Fatal(err)
	}
	tr := res.Tracks[0]
	if len(tr.Events) != 3 || tr.Clock.WrapModulus != 1<<32 {
		t.Fatalf("rtp parse wrong: %+v", tr)
	}
	if !tr.Events[0].Keyframe {
		t.Fatal("marker packet should be treated as keyframe")
	}
}

func TestRTPReorderAndTimestampWrap(t *testing.T) {
	// Deliver seq 1 before seq 0: adapter must reorder, not reject.
	p0 := NewRTPPacket(1, 0, u32sub(0, 7200), false, 16)
	p1 := NewRTPPacket(1, 1, 0, false, 16)
	w := NewRTPWriter()
	if err := w.WritePacket(p1); err != nil {
		t.Fatal(err)
	}
	if err := w.WritePacket(p0); err != nil {
		t.Fatal(err)
	}
	res, err := rtpA().Parse(bytes.NewReader(w.Bytes()), Options{Timescale: 90000})
	if err != nil {
		t.Fatalf("reordered packets must parse: %v", err)
	}
	evs := res.Tracks[0].Events
	if evs[0].Seq != 1 || evs[1].Seq != 0 {
		t.Fatal("events must follow sequence-number order")
	}
	if err := normalizeTracks(res.Tracks); err != nil {
		t.Fatal(err)
	}
	// After unwrap + normalize: wrapped packet is 80ms earlier than the second.
	delta := evs[1].TimeNS - evs[0].TimeNS
	if delta != 80_000_000 {
		t.Fatalf("wrap delta=%d want 80ms", delta)
	}
}

func TestRTPTruncatedAndSequenceGap(t *testing.T) {
	w := NewRTPWriter()
	_ = w.WritePacket(NewRTPPacket(1, 0, 0, false, 16))
	_ = w.WritePacket(NewRTPPacket(1, 2, 7200, false, 16)) // gap: seq 1 missing
	if _, err := rtpA().Parse(bytes.NewReader(w.Bytes()), Options{}); err == nil ||
		!strings.Contains(err.Error(), "gap") {
		t.Fatalf("sequence gap must be rejected, got %v", err)
	}
	good := NewRTPWriter()
	_ = good.WritePacket(NewRTPPacket(1, 0, 0, false, 16))
	raw := good.Bytes()
	if _, err := rtpA().Parse(bytes.NewReader(raw[:len(raw)-2]), Options{}); err == nil {
		t.Fatal("truncated RTP container must fail")
	}
}

// u32sub performs modular subtraction for 32-bit wrap test fixtures.
func u32sub(a, b uint32) uint32 { return a - b }

func normalizeTracks(tracks []timeline.Track) error {
	for i := range tracks {
		if err := timeline.NormalizeTrack(&tracks[i], 0, 0); err != nil {
			return err
		}
	}
	return nil
}
