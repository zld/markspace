package frame

import (
	"errors"
	"testing"

	"rtspcheck/internal/rtp"
)

func mkPkt(ssrc uint32, ts uint32, seq uint16, marker bool) *rtp.Packet {
	return &rtp.Packet{
		SSRC:           ssrc,
		Timestamp:      ts,
		SequenceNumber: seq,
		Marker:         marker,
		Payload:        []byte{1, 2, 3, 4},
	}
}

func TestAssemblerCompleteFrameOnMarker(t *testing.T) {
	a := NewAssembler(10, 10)
	if out, _ := a.Push(mkPkt(1, 100, 0, false)); len(out) != 0 {
		t.Fatalf("first packet must stay buffered, got %d frames", len(out))
	}
	if out, _ := a.Push(mkPkt(1, 100, 1, false)); len(out) != 0 {
		t.Fatalf("non-marker packet must stay buffered")
	}
	out, err := a.Push(mkPkt(1, 100, 2, true))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || !out[0].Complete {
		t.Fatalf("marker must close one complete frame, got %+v", out)
	}
	if out[0].Size != 12 || len(out[0].Packets) != 3 {
		t.Fatalf("frame accounting wrong: size=%d packets=%d", out[0].Size, len(out[0].Packets))
	}
	if a.Stats().Complete != 1 || a.Stats().Incomplete != 0 || a.Pending() != 0 {
		t.Fatalf("unexpected stats: %+v", a.Stats())
	}
}

func TestAssemblerTimestampSwitchWithoutMarkerIsIncomplete(t *testing.T) {
	a := NewAssembler(10, 10)
	a.Push(mkPkt(1, 100, 0, false))
	a.Push(mkPkt(1, 100, 1, false))
	out, _ := a.Push(mkPkt(1, 200, 2, false)) // new timestamp, no marker on 100
	if len(out) != 1 || out[0].Complete || out[0].Timestamp != 100 {
		t.Fatalf("expected one incomplete frame ts=100, got %+v", out)
	}
	if a.Stats().Incomplete != 1 {
		t.Fatalf("incomplete = %d, want 1", a.Stats().Incomplete)
	}
}

func TestAssemblerIsolatesSSRC(t *testing.T) {
	a := NewAssembler(10, 10)
	a.Push(mkPkt(1, 100, 0, false))
	a.Push(mkPkt(2, 100, 0, false)) // same ts, different SSRC
	a.Push(mkPkt(1, 100, 1, true))
	if a.Pending() != 1 {
		t.Fatalf("one frame should remain open for SSRC 2, pending=%d", a.Pending())
	}
	out := a.Flush()
	if len(out) != 1 || out[0].SSRC != 2 || out[0].Complete {
		t.Fatalf("flush should return incomplete SSRC-2 frame, got %+v", out)
	}
}

func TestAssemblerFlushMarksEverythingIncomplete(t *testing.T) {
	a := NewAssembler(10, 10)
	a.Push(mkPkt(1, 100, 0, false))
	a.Push(mkPkt(2, 200, 0, false))
	out := a.Flush()
	if len(out) != 2 {
		t.Fatalf("flush returned %d frames, want 2", len(out))
	}
	for _, f := range out {
		if f.Complete {
			t.Fatalf("flushed frame %d must be incomplete", f.SSRC)
		}
	}
	if out[0].SSRC != 1 || out[1].SSRC != 2 {
		t.Fatalf("flush order must be sorted by SSRC, got %d,%d", out[0].SSRC, out[1].SSRC)
	}
	if a.Pending() != 0 || a.Stats().Incomplete != 2 {
		t.Fatalf("post-flush state wrong: %+v pending=%d", a.Stats(), a.Pending())
	}
}

func TestAssemblerPerFramePacketLimit(t *testing.T) {
	a := NewAssembler(2, 10)
	a.Push(mkPkt(1, 100, 0, false))
	a.Push(mkPkt(1, 100, 1, false))
	out, err := a.Push(mkPkt(1, 100, 2, false)) // third packet over cap
	if !errors.Is(err, ErrBufferLimit) {
		t.Fatalf("got %v, want ErrBufferLimit", err)
	}
	if len(out) != 1 || out[0].Complete {
		t.Fatalf("over-cap frame must be emitted incomplete, got %+v", out)
	}
	if a.Stats().Incomplete != 1 || a.Pending() != 1 {
		t.Fatalf("new frame should be open: %+v pending=%d", a.Stats(), a.Pending())
	}
}

func TestAssemblerBufferedFrameLimitEvictsOldest(t *testing.T) {
	a := NewAssembler(10, 2)
	a.Push(mkPkt(1, 100, 0, false))
	a.Push(mkPkt(2, 100, 0, false))
	out, _ := a.Push(mkPkt(3, 100, 0, false)) // third concurrent SSRC
	if len(out) != 1 || out[0].SSRC != 1 || out[0].Complete {
		t.Fatalf("oldest frame must be evicted incomplete, got %+v", out)
	}
	if a.Stats().Evicted != 1 || a.Pending() != 2 {
		t.Fatalf("eviction accounting wrong: %+v", a.Stats())
	}
}
