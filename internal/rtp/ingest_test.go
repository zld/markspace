package rtp

import (
	"testing"

	"github.com/pion/rtp"

	"medcheck/internal/clock"
)

func marshalTestPacket(t *testing.T, ssrc uint32, seq uint16, pt uint8, payload []byte) []byte {
	t.Helper()
	b, err := (&rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: pt, SSRC: ssrc, SequenceNumber: seq, Timestamp: uint32(seq) * 3000},
		Payload: payload,
	}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestIngestorLocatesInvalidPacketByOffset(t *testing.T) {
	in := NewIngestor()
	in.Register(100, clock.StreamAudio)
	good := marshalTestPacket(t, 100, 4, 97, []byte("audio"))
	if _, errs := in.Ingest(good, 128); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	bad := append([]byte{}, good...)
	bad[0] = 0x00 // invalid RTP version
	_, errs := in.Ingest(bad, 384)
	if len(errs) != 1 {
		t.Fatalf("errs=%v", errs)
	}
	if errs[0].Code != clock.CodeInvalidPacket || errs[0].InputOffset != 384 {
		t.Fatalf("bad error: %+v", errs[0])
	}
	if errs[0].SSRC != 100 || errs[0].Sequence != 4 {
		t.Fatalf("partial header location lost: %+v", errs[0])
	}
}

func TestIngestorCountsDuplicateAndReorder(t *testing.T) {
	in := NewIngestor()
	in.Register(200, clock.StreamVideo)
	p1 := marshalTestPacket(t, 200, 7, 96, []byte{1})
	p2 := marshalTestPacket(t, 200, 8, 96, []byte{2})
	p2Old := marshalTestPacket(t, 200, 6, 96, []byte{0})
	for i, p := range [][]byte{p1, p2, p1, p2Old} {
		in.Ingest(p, int64(i*64))
	}
	if in.Stats.Duplicates != 1 || in.Stats.Reordered != 1 {
		t.Fatalf("stats=%+v", in.Stats)
	}
}
