package rtp

import (
	"testing"

	"github.com/pion/rtp"
)

func h264Packet(ssrc uint32, pt uint8, ts uint32, marker bool, payload []byte) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			SSRC:           ssrc,
			PayloadType:    pt,
			Timestamp:      ts,
			Marker:         marker,
			SequenceNumber: 1,
		},
		Payload: payload,
	}
}

func TestH264SingleNAL(t *testing.T) {
	r := NewReassembler()
	nal := []byte{0x65, 0xAA, 0xBB}
	frame, err := r.Process(h264Packet(1, 96, 9000, true, nal))
	if err != nil {
		t.Fatal(err)
	}
	if frame == nil || len(frame.NALUs) != 1 {
		t.Fatalf("expected one frame with one NAL, got %+v", frame)
	}
	if frame.Size != len(nal) {
		t.Fatalf("size = %d, want %d", frame.Size, len(nal))
	}
	if r.Stats().ReassembledFrames != 1 {
		t.Fatalf("reassembled = %d, want 1", r.Stats().ReassembledFrames)
	}
}

func TestH264STAPA(t *testing.T) {
	r := NewReassembler()
	n1 := []byte{0x67, 1, 2, 3}
	n2 := []byte{0x68, 4, 5}
	payload := []byte{0x18}
	payload = append(payload, byte(len(n1)>>8), byte(len(n1)))
	payload = append(payload, n1...)
	payload = append(payload, byte(len(n2)>>8), byte(len(n2)))
	payload = append(payload, n2...)

	frame, err := r.Process(h264Packet(1, 96, 9000, true, payload))
	if err != nil {
		t.Fatal(err)
	}
	if frame == nil || len(frame.NALUs) != 2 {
		t.Fatalf("expected 2 NAL units, got %+v", frame)
	}
}

func TestH264FUAComplete(t *testing.T) {
	r := NewReassembler()
	indicator := byte(0x7C) // F=0, NRI=3, type=28
	nalType := byte(5)      // IDR
	// payload NAL too big for one RTP packet
	body := make([]byte, 3000)
	for i := range body {
		body[i] = byte(i)
	}
	chunk := 1400

	fu1 := []byte{indicator, 0x80 | nalType}
	fu1 = append(fu1, body[:chunk]...)
	p1 := h264Packet(1, 96, 9000, false, fu1)
	p1.SequenceNumber = 1
	if _, err := r.Process(p1); err != nil {
		t.Fatal(err)
	}
	fu2 := []byte{indicator, nalType}
	fu2 = append(fu2, body[chunk:2*chunk]...)
	p2 := h264Packet(1, 96, 9000, false, fu2)
	p2.SequenceNumber = 2
	if _, err := r.Process(p2); err != nil {
		t.Fatal(err)
	}
	fu3 := []byte{indicator, 0x40 | nalType}
	fu3 = append(fu3, body[2*chunk:]...)
	p3 := h264Packet(1, 96, 9000, true, fu3)
	p3.SequenceNumber = 3
	frame, err := r.Process(p3)
	if err != nil {
		t.Fatal(err)
	}
	if frame == nil || len(frame.NALUs) != 1 {
		t.Fatalf("expected reassembled frame, got %+v", frame)
	}
	nal := frame.NALUs[0]
	if nal[0] != (indicator&0xE0)|nalType {
		t.Fatalf("reconstructed header = %#x", nal[0])
	}
	if len(nal) != len(body)+1 {
		t.Fatalf("nal length = %d, want %d", len(nal), len(body)+1)
	}
}

func TestH264FUAMissingFragment(t *testing.T) {
	r := NewReassembler()
	indicator, nalType := byte(0x7C), byte(5)
	fu1 := append([]byte{indicator, 0x80 | nalType}, 1, 2, 3)
	if _, err := r.Process(h264Packet(1, 96, 9000, false, fu1)); err != nil {
		t.Fatal(err)
	}
	// marker arrives without an ending fragment: frame must be discarded
	if _, err := r.Process(h264Packet(1, 96, 9000, true, []byte{0x65, 9})); err == nil {
		t.Fatal("expected error for unfinished FU-A")
	}
	st := r.Stats()
	if st.DiscardedFrames != 1 || st.MissingFragments != 1 {
		t.Fatalf("unexpected stats: %+v", st)
	}
}

func TestH264FUAMissingMiddleFragment(t *testing.T) {
	r := NewReassembler()
	indicator, nalType := byte(0x7C), byte(5)
	// packets carry consecutive sequence numbers, but seq 2 is lost
	fu1 := h264Packet(1, 96, 9000, false, append([]byte{indicator, 0x80 | nalType}, 1))
	fu1.SequenceNumber = 1
	if _, err := r.Process(fu1); err != nil {
		t.Fatal(err)
	}
	fu3 := h264Packet(1, 96, 9000, true, append([]byte{indicator, 0x40 | nalType}, 3))
	fu3.SequenceNumber = 3
	if _, err := r.Process(fu3); err == nil {
		t.Fatal("expected missing-fragment error")
	}
	st := r.Stats()
	if st.MissingFragments != 1 || st.DiscardedFrames != 1 || st.ReassembledFrames != 0 {
		t.Fatalf("unexpected stats: %+v", st)
	}
}

func TestH264IllegalFUStart(t *testing.T) {
	r := NewReassembler()
	indicator, nalType := byte(0x7C), byte(5)
	// a continuation/end fragment without any preceding start
	bad := []byte{indicator, 0x40 | nalType, 1, 2, 3}
	if _, err := r.Process(h264Packet(1, 96, 9000, true, bad)); err == nil {
		t.Fatal("expected illegal FU-A error")
	}
	st := r.Stats()
	if st.IllegalFUHeaders != 1 || st.DiscardedFrames != 1 {
		t.Fatalf("unexpected stats: %+v", st)
	}
}

func TestH264OversizedFrame(t *testing.T) {
	r := NewReassembler()
	r.maxSize = 16 // shrink the limit for the test
	nal := make([]byte, 32)
	nal[0] = 0x65
	if _, err := r.Process(h264Packet(1, 96, 9000, true, nal)); err == nil {
		t.Fatal("expected oversize error")
	}
	st := r.Stats()
	if st.OversizedFrames != 1 || st.DiscardedFrames != 1 {
		t.Fatalf("unexpected stats: %+v", st)
	}
}

func TestH264KeyIsolation(t *testing.T) {
	r := NewReassembler()
	// two senders interleave fragments of the same timestamp
	p1 := h264Packet(1, 96, 9000, false, []byte{0x7C, 0x85, 1})
	p1.SequenceNumber = 10
	p2 := h264Packet(2, 96, 9000, false, []byte{0x7C, 0x85, 9})
	p2.SequenceNumber = 20
	p3 := h264Packet(1, 96, 9000, true, []byte{0x7C, 0x45, 2})
	p3.SequenceNumber = 11
	p4 := h264Packet(2, 96, 9000, true, []byte{0x7C, 0x45, 8})
	p4.SequenceNumber = 21
	_, _ = r.Process(p1)
	_, _ = r.Process(p2)
	f1, _ := r.Process(p3)
	f2, _ := r.Process(p4)
	if f1 == nil || f2 == nil || f1.Key.SSRC == f2.Key.SSRC {
		t.Fatalf("expected two isolated frames, got %+v %+v", f1, f2)
	}
}

func TestH264FlushDiscardsOpenFU(t *testing.T) {
	r := NewReassembler()
	_, _ = r.Process(h264Packet(1, 96, 9000, false, []byte{0x7C, 0x85, 1}))
	frames := r.Flush()
	if len(frames) != 0 {
		t.Fatalf("expected no frames, got %d", len(frames))
	}
	st := r.Stats()
	if st.MissingFragments != 1 || st.DiscardedFrames != 1 {
		t.Fatalf("unexpected stats: %+v", st)
	}
}
