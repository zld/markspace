package rtp

import (
	"testing"

	"github.com/pion/rtp"
)

func h264pkt(seq uint16, ts uint32, ssrc uint32, marker bool, payload []byte) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			Marker:         marker,
			PayloadType:    96,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           ssrc,
		},
		Payload: payload,
	}
}

func TestSingleNALReassembly(t *testing.T) {
	h := NewH264Reassembler(MaxBufferedAUs)
	nal := []byte{0x65, 1, 2, 3, 4} // type 5 (IDR slice)
	frame, err := h.Process(h264pkt(0, 1000, 1, true, nal))
	if err != nil {
		t.Fatal(err)
	}
	if len(frame) != 1 || string(frame[0]) != string(nal) {
		t.Fatalf("want single NAL frame, got %+v", frame)
	}
	if s := h.Stats(); s.FramesReassembled != 1 || s.FramesDropped != 0 {
		t.Fatalf("bad stats: %+v", s)
	}
}

func TestFUAReassembly(t *testing.T) {
	h := NewH264Reassembler(MaxBufferedAUs)
	orig := []byte{0x65, 0xaa, 0xbb, 0xcc, 0xdd, 0xee} // NAL type 5
	// FU indicator keeps high 3 bits (0x60), FU header carries type 5.
	start := []byte{0x7c, 0x85, 0xaa, 0xbb} // start bit, type 5
	mid := []byte{0x7c, 0x05, 0xcc}
	end := []byte{0x7c, 0x45, 0xdd, 0xee} // end bit

	if _, err := h.Process(h264pkt(0, 9000, 1, false, start)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Process(h264pkt(1, 9000, 1, false, mid)); err != nil {
		t.Fatal(err)
	}
	frame, err := h.Process(h264pkt(2, 9000, 1, true, end))
	if err != nil {
		t.Fatal(err)
	}
	if len(frame) != 1 || string(frame[0]) != string(orig) {
		t.Fatalf("reassembled FU mismatch: got % x want % x", frame, orig)
	}
}

func TestSTAPAReassembly(t *testing.T) {
	h := NewH264Reassembler(MaxBufferedAUs)
	nal1 := []byte{0x67, 0x11, 0x22} // SPS
	nal2 := []byte{0x68, 0x33}       // PPS
	payload := []byte{nalTypeSTAPA}
	payload = append(payload, 0x00, byte(len(nal1)))
	payload = append(payload, nal1...)
	payload = append(payload, 0x00, byte(len(nal2)))
	payload = append(payload, nal2...)

	frame, err := h.Process(h264pkt(0, 500, 1, true, payload))
	if err != nil {
		t.Fatal(err)
	}
	if len(frame) != 2 || string(frame[0]) != string(nal1) || string(frame[1]) != string(nal2) {
		t.Fatalf("STAP-A must split into two NALs, got %+v", frame)
	}
}

func TestMissingFUFragmentDropsFrame(t *testing.T) {
	h := NewH264Reassembler(MaxBufferedAUs)
	start := []byte{0x7c, 0x85, 0xaa, 0xbb}
	end := []byte{0x7c, 0x45, 0xdd, 0xee}
	// seq 0 start, seq 1 lost, seq 2 end: the gap marks the FU corrupt.
	if _, err := h.Process(h264pkt(0, 9000, 1, false, start)); err != nil {
		t.Fatal(err)
	}
	frame, err := h.Process(h264pkt(2, 9000, 1, true, end))
	if err != nil {
		t.Fatal(err)
	}
	if frame != nil {
		t.Fatalf("corrupted FU must not produce a frame, got %+v", frame)
	}
	s := h.Stats()
	if s.FramesDropped != 1 || s.DropsByReason[DropMissingFragment] != 1 {
		t.Fatalf("want one missing-fragment drop, got %+v", s)
	}
}

func TestSSRCChangeDropsPartial(t *testing.T) {
	h := NewH264Reassembler(MaxBufferedAUs)
	start := []byte{0x7c, 0x85, 0xaa}
	if _, err := h.Process(h264pkt(0, 9000, 1, false, start)); err != nil {
		t.Fatal(err)
	}
	frame, err := h.Process(h264pkt(1, 9000, 2, true, []byte{0x65, 1}))
	if err != nil {
		t.Fatal(err)
	}
	if len(frame) != 1 {
		t.Fatalf("new SSRC packet must still deliver its own frame, got %+v", frame)
	}
	if h.Stats().DropsByReason[DropSSRCChange] != 1 {
		t.Fatalf("want one ssrc_change drop, got %+v", h.Stats())
	}
}

func TestTimestampJumpFinalizesAndDropsOpenFU(t *testing.T) {
	h := NewH264Reassembler(MaxBufferedAUs)
	start := []byte{0x7c, 0x85, 0xaa}
	if _, err := h.Process(h264pkt(0, 9000, 1, false, start)); err != nil {
		t.Fatal(err)
	}
	// New timestamp before the FU end arrived: interrupted frame dropped.
	frame, err := h.Process(h264pkt(1, 18000, 1, true, []byte{0x65, 2}))
	if err != nil {
		t.Fatal(err)
	}
	if len(frame) != 1 || frame[0][0] != 0x65 {
		t.Fatalf("new timestamp frame must be delivered, got %+v", frame)
	}
	s := h.Stats()
	if s.FramesReassembled != 1 || s.DropsByReason[DropTimestampJump] != 1 {
		t.Fatalf("want one timestamp-jump drop, got %+v", s)
	}
}

func TestMalformedSTAPARejected(t *testing.T) {
	h := NewH264Reassembler(MaxBufferedAUs)
	bad := [][]byte{
		{nalTypeSTAPA, 0, 0, 5, 1, 2},             // declared size exceeds payload
		{nalTypeSTAPA, 0, 0},                      // truncated size field
	}
	for i, p := range bad {
		if _, err := h.Process(h264pkt(uint16(i), uint32(i*1000), 1, true, p)); err == nil {
			t.Fatalf("case %d: malformed STAP-A must error", i)
		}
	}
}
