package rtpx_test

import (
	"testing"

	"github.com/pion/rtp"

	"mediacore/internal/rtpx"
)

func mustPacket(t *testing.T, seq uint16, ts uint32) []byte {
	t.Helper()
	p := rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: seq, Timestamp: ts, SSRC: 1},
		Payload: []byte{0x00, 0x01},
	}
	b, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTimestampMapping(t *testing.T) {
	m, err := rtpx.NewTimestampMapper(90000, 0)
	if err != nil {
		t.Fatal(err)
	}
	ext, u, err := m.Map(90000)
	if err != nil || ext != 90000 || u != 1_000_000_000 {
		t.Fatalf("map 90000: ext=%d u=%d err=%v", ext, u, err)
	}
}

func TestTimestampWrapExtension(t *testing.T) {
	m, err := rtpx.NewTimestampMapper(90000, 0)
	if err != nil {
		t.Fatal(err)
	}
	m.Unwrap(^uint32(0))
	ext := m.Unwrap(0)
	if ext != uint64(^uint32(0))+1 {
		t.Fatalf("wrapped ext=%d", ext)
	}
}

func TestVerifyPacketOrderWrapDuplicateAndReorder(t *testing.T) {
	wrap, err := rtpx.VerifyPackets([][]byte{
		mustPacket(t, 65535, 1),
		mustPacket(t, 0, 2),
		mustPacket(t, 0, 2),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !wrap.Ordered || len(wrap.Duplicates) != 1 || wrap.MissingCount != 0 {
		t.Fatalf("wrap report=%+v", wrap)
	}

	reorder, err := rtpx.VerifyPackets([][]byte{
		mustPacket(t, 2, 1),
		mustPacket(t, 1, 2),
	})
	if err != nil {
		t.Fatal(err)
	}
	if reorder.Ordered || len(reorder.OutOfOrder) != 1 {
		t.Fatalf("reorder report=%+v", reorder)
	}
}
