package rtp

import (
	"math"
	"testing"

	"github.com/pion/rtp"

	"mediasync/internal/clock"
)

func buildDatagram(t *testing.T, ssrc uint32, seq uint16, ts uint32) []byte {
	t.Helper()
	p := &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: seq, Timestamp: ts, SSRC: ssrc},
		Payload: []byte{0xAA, 0xBB},
	}
	data, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestIngestOrderingAndCounters(t *testing.T) {
	g, err := NewIngestor(11, clock.DefaultClockRate, 4)
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately shuffled sequence 1..4.
	for _, spec := range []struct{ seq uint16; ts uint32 }{
		{1, 9000}, {2, 18000}, {4, 36000}, {3, 27000},
	} {
		if _, e := g.IngestRaw(buildDatagram(t, 11, spec.seq, spec.ts)); e != nil {
			t.Fatalf("ingest seq %d failed: %v", spec.seq, e)
		}
	}
	frames, e := g.Ordered()
	if e != nil || len(frames) != 4 {
		t.Fatalf("ordered frames=%d err=%v", len(frames), e)
	}
	for i, f := range frames {
		if f.Packet.SequenceNumber != uint16(i+1) {
			t.Fatalf("frame %d seq %d not in order", i, f.Packet.SequenceNumber)
		}
		if f.EmitIndex != uint64(i) {
			t.Fatalf("frame %d emit index %d", i, f.EmitIndex)
		}
	}
	if g.Counters().Recovered != 1 {
		t.Fatalf("expected one recovery, got %+v", g.Counters())
	}
}

func TestIngestTimestampWrap(t *testing.T) {
	g, _ := NewIngestor(0, clock.DefaultClockRate, 4)
	near := uint32(math.MaxUint32 - 45000)
	for i, ts := range []uint32{near, near + 45000, near + 90000} {
		if _, e := g.IngestRaw(buildDatagram(t, 7, uint16(i+1), ts)); e != nil {
			t.Fatal(e)
		}
	}
	frames, _ := g.Finalize()
	if len(frames) != 3 || g.Wraps() != 1 {
		t.Fatalf("frames=%d wraps=%d", len(frames), g.Wraps())
	}
	// Extended timestamps must be strictly increasing across the wrap.
	if !(frames[0].ExtendedTS < frames[1].ExtendedTS && frames[1].ExtendedTS < frames[2].ExtendedTS) {
		t.Fatalf("extended timestamps not monotonic: %d %d %d",
			frames[0].ExtendedTS, frames[1].ExtendedTS, frames[2].ExtendedTS)
	}
	if frames[2].ExtendedTS-frames[0].ExtendedTS != 90000 {
		t.Fatalf("wrap span = %d want 90000", frames[2].ExtendedTS-frames[0].ExtendedTS)
	}
}

func TestIngestLengthGuard(t *testing.T) {
	g, _ := NewIngestor(1, clock.DefaultClockRate, 4)
	if _, e := g.IngestRaw(nil); e == nil || e.Kind != clock.ErrInvalidLength {
		t.Fatal("empty datagram not rejected")
	}
	if _, e := g.IngestRaw([]byte{0x80}); e == nil {
		t.Fatal("truncated datagram not rejected")
	}
	if g.FirstFailure() == nil {
		t.Fatal("first failure not retained for reporting")
	}
}

func TestIngestSSRCMismatch(t *testing.T) {
	g, _ := NewIngestor(99, clock.DefaultClockRate, 4)
	if _, e := g.IngestRaw(buildDatagram(t, 100, 1, 9000)); e == nil {
		t.Fatal("foreign SSRC packet accepted")
	}
}
