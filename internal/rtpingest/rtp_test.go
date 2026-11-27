package rtpingest

import (
	"context"
	"testing"

	"github.com/pion/rtp"

	"mediad/internal/ingest"
	"mediad/internal/model"
)

// datagram builds a marshaled RTP packet with the given fields and payload.
func datagram(seq uint16, ts uint32, marker bool, pt uint8, payload []byte) []byte {
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    pt,
			SequenceNumber: seq,
			Timestamp:      ts,
			Marker:         marker,
			SSRC:           42,
		},
		Payload: payload,
	}
	b, err := pkt.Marshal()
	if err != nil {
		panic(err)
	}
	return b
}

func TestParseValidAndInvalid(t *testing.T) {
	dg := datagram(7, 1000, true, 96, []byte{0x65, 1, 2, 3})
	tp, err := Parse(dg)
	if err != nil {
		t.Fatal(err)
	}
	if tp.SequenceNumber != 7 || tp.Timestamp != 1000 || !tp.Marker || tp.PayloadType != 96 || tp.SSRC != 42 {
		t.Fatalf("parsed fields wrong: %+v", tp)
	}
	// Corrupt the version bits (top two bits).
	bad := append([]byte(nil), dg...)
	bad[0] &^= 0xC0
	if _, err := Parse(bad); err == nil {
		t.Fatal("bad RTP version must be rejected")
	}
	if _, err := Parse([]byte{0x80}); err == nil {
		t.Fatal("truncated datagram must be rejected")
	}
}

func TestH264ReassemblyAndGap(t *testing.T) {
	b, err := NewAccessUnitBuilder(ingest.CodecH264)
	if err != nil {
		t.Fatal(err)
	}
	// Two single-NAL packets; the first is not a unit end, the second is.
	p1, _ := Parse(datagram(0, 1000, false, 96, []byte{0x67, 0x42}))
	if _, _, ok, err := b.Push(p1); err != nil || ok {
		t.Fatalf("first packet should not close a unit: ok=%v err=%v", ok, err)
	}
	p2, _ := Parse(datagram(1, 2000, true, 96, []byte{0x65, 0xAA, 0xBB}))
	payload, ts, ok, err := b.Push(p2)
	if err != nil || !ok {
		t.Fatalf("marker packet should close unit: ok=%v err=%v", ok, err)
	}
	if ts != 2000 || b.UnitCount() != 1 {
		t.Fatal("unit timestamp/count wrong")
	}
	// H264 depacketizer emits Annex-B framed NAL units.
	if len(payload) == 0 {
		t.Fatal("empty reassembled payload")
	}
	// Sequence gap must be detected.
	p3, _ := Parse(datagram(5, 3000, true, 96, []byte{0x65}))
	_, _, _, err = b.Push(p3)
	se, ok := model.AsStructured(err)
	if !ok || se.Code != model.ErrTruncatedUnit {
		t.Fatalf("want truncation error on sequence gap, got %v", err)
	}
}

func TestDepacketizerPerCodec(t *testing.T) {
	for _, codec := range []string{ingest.CodecH264, ingest.CodecH265, ingest.CodecAV1} {
		b, err := NewAccessUnitBuilder(codec)
		if err != nil {
			t.Fatalf("%s: %v", codec, err)
		}
		var payload []byte
		switch codec {
		case ingest.CodecH264:
			payload = []byte{0x65, 9}
		case ingest.CodecH265:
			payload = []byte{0x26, 0x01, 9}
		case ingest.CodecAV1:
			payload = []byte{0x32, 1, 9}
		}
		tp, _ := Parse(datagram(0, 0, true, 96, payload))
		out, _, done, err := b.Push(tp)
		if err != nil || !done || len(out) == 0 {
			t.Fatalf("%s reassembly failed: done=%v err=%v", codec, done, err)
		}
	}
	if _, err := NewAccessUnitBuilder("vp8"); err == nil {
		t.Fatal("unsupported codec must be rejected")
	}
}

func TestRTPToNS(t *testing.T) {
	// 90000 Hz clock: 90000 ticks == 1 s.
	ns, err := RTPToNS(90000, 90000)
	if err != nil || ns != 1_000_000_000 {
		t.Fatalf("RTPToNS: got %d err=%v", ns, err)
	}
	// 45000 ticks at 90kHz = 0.5 s.
	ns2, _ := RTPToNS(45000, 90000)
	if ns2 != 500_000_000 {
		t.Fatalf("half second: got %d", ns2)
	}
	if _, err := RTPToNS(1, 0); err == nil {
		t.Fatal("zero clock rate must be rejected")
	}
}

func TestDrainChannel(t *testing.T) {
	in := make(chan []byte, 3)
	in <- datagram(0, 0, true, 96, []byte{0x65, 1})
	in <- datagram(1, 1000, true, 96, []byte{0x65, 2})
	in <- datagram(2, 2000, true, 96, []byte{0x65, 3})
	close(in)
	units, err := Drain(context.Background(), ingest.CodecH264, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 3 {
		t.Fatalf("want 3 units, got %d", len(units))
	}
}

func TestDrainCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	in := make(chan []byte)
	cancel()
	if _, err := Drain(ctx, ingest.CodecH264, in); err == nil {
		t.Fatal("canceled drain must return an error")
	}
}
