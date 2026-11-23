package transport

import "testing"

func frag(ssrc uint32, seq uint16, ts uint32, payload []byte, marker bool) *Packet {
	return &Packet{SSRC: ssrc, Seq: seq, Timestamp: ts, Payload: payload, Marker: marker}
}

func TestReassembleMarkerFrame(t *testing.T) {
	g := NewReassembler(5)
	if f, err := g.Push(frag(5, 0, 1000, []byte{0x00, 0x00}, false)); err != nil || f != nil {
		t.Fatalf("first fragment should buffer: f=%v err=%v", f, err)
	}
	if f, err := g.Push(frag(5, 1, 1000, []byte{0x01}, false)); err != nil || f != nil {
		t.Fatalf("middle fragment should buffer: %v", err)
	}
	f, err := g.Push(frag(5, 2, 1000, []byte{0x02}, true))
	if err != nil || f == nil {
		t.Fatalf("marker must close frame: %v", err)
	}
	if !f.Complete || string(f.Bytes) != "\x00\x00\x01\x02" ||
		f.FirstSeq != 0 || f.LastSeq != 2 || f.Timestamp != 1000 {
		t.Fatalf("frame content wrong: %+v", f)
	}
}

func TestReassembleGapTruncates(t *testing.T) {
	g := NewReassembler(5)
	g.Push(frag(5, 10, 900, []byte{0xAA}, false))
	g.Push(frag(5, 11, 900, []byte{0xBB}, false))
	// Reorderer reports a burst gap: the frame never received its marker.
	f := g.Gap()
	if f == nil || f.Complete {
		t.Fatalf("gap must yield an incomplete frame: %+v", f)
	}
	if string(f.Bytes) != "\xAA\xBB" || f.Timestamp != 900 {
		t.Fatalf("partial bytes not preserved: %+v", f)
	}
	// A fresh frame after the gap starts clean.
	f2, err := g.Push(frag(5, 15, 1800, []byte{0xCC}, true))
	if err != nil || f2 == nil || !f2.Complete || f2.FirstSeq != 15 {
		t.Fatalf("post-gap frame broken: %+v err=%v", f2, err)
	}
}

func TestReassembleFlushAtEnd(t *testing.T) {
	g := NewReassembler(5)
	g.Push(frag(5, 0, 100, []byte{1}, false))
	f := g.Flush()
	if f == nil || f.Complete {
		t.Fatalf("unterminated stream end must flush incomplete frame: %+v", f)
	}
	if g.Flush() != nil {
		t.Fatal("second flush must be empty after reset")
	}
}

func TestReassembleFrameSizeCap(t *testing.T) {
	g := NewReassembler(5)
	g.maxFrame = 4
	g.Push(frag(5, 0, 1, []byte{1, 2}, false))
	if _, err := g.Push(frag(5, 1, 1, []byte{3, 4, 5}, false)); err == nil {
		t.Fatal("oversized frame accepted")
	}
}
