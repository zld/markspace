package rtp

import "testing"

func framePkt(ssrc uint32, seq uint16, ts uint32, marker bool) *Packet {
	return &Packet{
		Header: Header{
			SSRC: ssrc, SequenceNumber: seq, Timestamp: ts, Marker: marker,
		},
		Payload: []byte{byte(seq)},
	}
}

func TestFrameCompleteOnMarker(t *testing.T) {
	rm := NewReassembler()
	rm.Push(framePkt(1, 0, 100, false))
	rm.Push(framePkt(1, 1, 100, false))
	out := rm.Push(framePkt(1, 2, 100, true))
	if len(out) != 1 || !out[0].Complete || len(out[0].Packets) != 3 {
		t.Fatalf("frame not closed correctly: %+v", out)
	}
	if out[0].FirstSeq != 0 || out[0].LastSeq != 2 {
		t.Fatalf("frame sequence bounds wrong: %+v", out[0])
	}
	if rm.Stats().CompleteFrames != 1 || rm.Stats().IncompleteFrames != 0 {
		t.Fatalf("stats wrong: %+v", rm.Stats())
	}
}

func TestFrameIncompleteOnTimestampSwitch(t *testing.T) {
	rm := NewReassembler()
	rm.Push(framePkt(1, 0, 100, false))
	rm.Push(framePkt(1, 1, 100, false)) // no marker
	out := rm.Push(framePkt(1, 2, 200, true))
	if len(out) != 2 {
		t.Fatalf("expected incomplete + new complete frame, got %d", len(out))
	}
	if out[0].Complete || out[0].Timestamp != 100 {
		t.Fatalf("first frame must be incomplete: %+v", out[0])
	}
	if !out[1].Complete || out[1].Timestamp != 200 {
		t.Fatalf("second frame must be complete: %+v", out[1])
	}
	if rm.Stats().CompleteFrames != 1 || rm.Stats().IncompleteFrames != 1 {
		t.Fatalf("stats wrong: %+v", rm.Stats())
	}
}

func TestFrameSSRCIsolation(t *testing.T) {
	rm := NewReassembler()
	rm.Push(framePkt(10, 0, 100, false))
	rm.Push(framePkt(20, 0, 100, false))
	out := rm.Push(framePkt(10, 1, 100, true))
	if len(out) != 1 || out[0].SSRC != 10 || len(out[0].Packets) != 2 {
		t.Fatalf("SSRC isolation broken: %+v", out)
	}
	// SSRC 20 still has an open, unclosed frame.
	if rm.streams[20].frame == nil || len(rm.streams[20].frame.Packets) != 1 {
		t.Fatalf("other stream frame lost")
	}
}

func TestFrameResetFlushesIncomplete(t *testing.T) {
	rm := NewReassembler()
	rm.Push(framePkt(1, 0, 100, false))
	rm.Push(framePkt(2, 0, 200, false))
	flushed := rm.Reset()
	if len(flushed) != 2 {
		t.Fatalf("expected 2 flushed open frames, got %d", len(flushed))
	}
	for _, fr := range flushed {
		if fr.Complete {
			t.Fatalf("reset must mark frames incomplete: %+v", fr)
		}
	}
	if len(rm.streams) != 0 || rm.Stats().IncompleteFrames != 2 {
		t.Fatalf("reset residue: streams=%d stats=%+v", len(rm.streams), rm.Stats())
	}
	// Next run starts clean: same SSRC begins a fresh frame.
	out := rm.Push(framePkt(1, 0, 300, true))
	if len(out) != 1 || !out[0].Complete || len(out[0].Packets) != 1 {
		t.Fatalf("post-reset frame wrong: %+v", out)
	}
}

func TestFrameMultipleComplete(t *testing.T) {
	rm := NewReassembler()
	first := rm.Push(framePkt(1, 0, 100, true))
	if len(first) != 1 || !first[0].Complete {
		t.Fatalf("first marker frame wrong: %+v", first)
	}
	rm.Push(framePkt(1, 1, 200, false))
	out := rm.Push(framePkt(1, 2, 200, true))
	if len(out) != 1 || !out[0].Complete {
		t.Fatalf("expected one complete frame: %+v", out)
	}
	if rm.Stats().CompleteFrames != 2 {
		t.Fatalf("stats wrong: %+v", rm.Stats())
	}
}
