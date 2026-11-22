package rtp

import (
	"testing"

	"github.com/pion/rtp"
)

func pkt(ssrc uint32, seq uint16, ts uint32, marker bool, payload byte) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           ssrc,
			Marker:         marker,
		},
		Payload: []byte{payload},
	}
}

// Independent small sample from the acceptance description:
// single SSRC, seq 65534..1, same timestamp, marker only on seq 1.
func TestAssemblerWraparoundCompleteFrame(t *testing.T) {
	a := NewAssembler()
	for _, p := range []*rtp.Packet{
		pkt(1, 65534, 500, false, 0xa),
		pkt(1, 65535, 500, false, 0xb),
		pkt(1, 0, 500, false, 0xc),
		pkt(1, 1, 500, true, 0xd),
	} {
		a.Push(p)
	}
	a.Finalize()
	complete, incomplete := a.Stats()
	if complete != 1 || incomplete != 0 {
		t.Fatalf("complete=%d incomplete=%d, want 1/0", complete, incomplete)
	}
	if a.GapCount() != 0 {
		t.Fatalf("gaps=%v, want none", a.Gaps())
	}
	f := a.Frames()[0]
	if string(f.Payload) != string([]byte{0xa, 0xb, 0xc, 0xd}) {
		t.Fatalf("payload order across wrap = %x, want abcd", f.Payload)
	}
	if !f.Complete || f.PacketCount != 4 {
		t.Fatalf("frame = %+v", f)
	}
}

// Reordered arrival of a complete frame must still reassemble fully.
func TestAssemblerReorderedCompleteFrame(t *testing.T) {
	a := NewAssembler()
	for _, p := range []*rtp.Packet{
		pkt(1, 11, 100, false, 0x2),
		pkt(1, 10, 100, false, 0x1),
		pkt(1, 12, 100, true, 0x3),
	} {
		a.Push(p)
	}
	a.Finalize()
	complete, incomplete := a.Stats()
	if complete != 1 || incomplete != 0 {
		t.Fatalf("complete=%d incomplete=%d, want 1/0", complete, incomplete)
	}
	if a.GapCount() != 0 {
		t.Fatalf("unexpected gaps: %v", a.Gaps())
	}
	f := a.Frames()[0]
	if string(f.Payload) != string([]byte{0x1, 0x2, 0x3}) {
		t.Fatalf("reordered payload = %x, want 010203", f.Payload)
	}
}

// Recovery case: a complete reordered frame, then a lost packet and
// arrival of the next-timestamp frame. The previous frame counts as
// incomplete with one gap; the later frame still reassembles complete;
// cumulative sequence checking is not reset by the bad frame.
func TestAssemblerCrossFrameGapAndRecovery(t *testing.T) {
	a := NewAssembler()

	// Frame A (ts 100): seq 20,21,22 reordered, marker on 22, complete.
	for _, p := range []*rtp.Packet{
		pkt(1, 21, 100, false, 0x2),
		pkt(1, 20, 100, false, 0x1),
		pkt(1, 22, 100, true, 0x3),
	} {
		a.Push(p)
	}

	// Frame B (ts 200): seq 23 arrives, seq 24 lost, then frame C's
	// first packet (seq 25, ts 300) arrives without a marker for B.
	a.Push(pkt(1, 23, 200, false, 0x4))
	a.Push(pkt(1, 25, 300, false, 0x5))
	// Frame C completes normally.
	a.Push(pkt(1, 26, 300, true, 0x6))

	// The real loss (seq 24) is charged at stream finalization.
	a.Finalize()

	complete, incomplete := a.Stats()
	if complete != 2 || incomplete != 1 {
		t.Fatalf("complete=%d incomplete=%d, want 2/1", complete, incomplete)
	}
	if a.GapCount() != 1 {
		t.Fatalf("gap count = %d, want 1 (%v)", a.GapCount(), a.Gaps())
	}

	frames := a.Frames()
	if len(frames) != 3 {
		t.Fatalf("frame count = %d, want 3", len(frames))
	}
	if !frames[0].Complete || frames[0].Timestamp != 100 {
		t.Fatalf("frame A wrong: %+v", frames[0])
	}
	if frames[1].Complete || frames[1].Timestamp != 200 ||
		frames[1].GapCount != 1 || string(frames[1].Payload) != "\x04" {
		t.Fatalf("frame B should be incomplete with one gap: %+v", frames[1])
	}
	if !frames[2].Complete || frames[2].Timestamp != 300 ||
		string(frames[2].Payload) != "\x05\x06" {
		t.Fatalf("frame C should still be complete: %+v", frames[2])
	}

	// Later-timestamp data must never be merged into the bad frame.
	if frames[1].PacketCount != 1 {
		t.Fatalf("bad frame absorbed %d packets, want 1", frames[1].PacketCount)
	}
}

func TestAssemblerSSRCIsolation(t *testing.T) {
	a := NewAssembler()
	a.Push(pkt(100, 0, 10, false, 0x1))
	a.Push(pkt(200, 0, 10, false, 0x9))
	a.Push(pkt(100, 1, 10, true, 0x2))
	a.Push(pkt(200, 1, 10, true, 0xa))
	a.Finalize()

	complete, incomplete := a.Stats()
	if complete != 2 || incomplete != 0 {
		t.Fatalf("complete=%d incomplete=%d, want 2/0", complete, incomplete)
	}
	for _, f := range a.Frames() {
		if f.SSRC != 100 && f.SSRC != 200 {
			t.Fatalf("unexpected ssrc %d", f.SSRC)
		}
		if f.SSRC == 100 && string(f.Payload) != "\x01\x02" {
			t.Fatalf("SSRC payloads mixed: %x", f.Payload)
		}
	}
}

func TestAssemblerFlushOpenAtEndOfStream(t *testing.T) {
	a := NewAssembler()
	a.Push(pkt(1, 0, 77, false, 0x1))
	a.Push(pkt(1, 1, 77, false, 0x2)) // no marker: connection dropped
	open := a.FlushOpen()
	if len(open) != 1 || open[0].Complete {
		t.Fatalf("open frame should flush incomplete: %+v", open)
	}
	complete, incomplete := a.Stats()
	if complete != 0 || incomplete != 1 {
		t.Fatalf("complete=%d incomplete=%d, want 0/1", complete, incomplete)
	}
}

func TestAssemblerEndStreamTailGap(t *testing.T) {
	// A complete frame, then a last frame whose single observed packet
	// is seq 5 while the true tail is seq 6 (one lost packet).
	a := NewAssembler()
	a.Push(pkt(1, 3, 100, false, 0x1))
	a.Push(pkt(1, 4, 100, true, 0x2)) // complete frame
	a.Push(pkt(1, 5, 200, false, 0x3)) // last frame, no marker
	a.EndStream(1, 6)

	complete, incomplete := a.Stats()
	if complete != 1 || incomplete != 1 {
		t.Fatalf("complete=%d incomplete=%d, want 1/1", complete, incomplete)
	}
	if a.GapCount() != 1 {
		t.Fatalf("gap count = %d, want 1", a.GapCount())
	}
	frames := a.Frames()
	if frames[1].Complete || frames[1].GapCount != 1 || frames[1].Timestamp != 200 {
		t.Fatalf("tail gap not attributed to last frame: %+v", frames[1])
	}
}
