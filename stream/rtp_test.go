package stream

import (
	"context"
	"testing"

	"github.com/pion/rtp"

	"github.com/zld/media-regress/internal/error"
)

func rtpItem(index int, ts uint32, payload int) Item {
	return Item{
		Index: index, TrackID: 2, Timestamp: ts, Sequence: uint16(index - 1),
		Payload: make([]byte, payload), Channels: 2, InputOffset: int64(index) * 100,
	}
}

func TestMarshalPacketRoundTrip(t *testing.T) {
	it := rtpItem(1, 8000, 32000)
	raw, err := MarshalPacket(it, false)
	if err != nil {
		t.Fatal(err)
	}
	seq, ts, payload, err := DecodePacket(raw, 2, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if seq != it.Sequence || ts != it.Timestamp || len(payload) != 32000 {
		t.Fatalf("round trip: seq=%d ts=%d len=%d", seq, ts, len(payload))
	}
}

func TestMarshalPacketMarker(t *testing.T) {
	raw, err := MarshalPacket(rtpItem(1, 0, 16), true)
	if err != nil {
		t.Fatal(err)
	}
	p := &rtp.Packet{}
	if err := p.Unmarshal(raw); err != nil {
		t.Fatal(err)
	}
	if !p.Marker {
		t.Fatal("marker bit not set on last block")
	}
	if p.Version != 2 || p.SSRC != rtpSSRC(2) {
		t.Fatalf("header %+v", p.Header)
	}
}

func TestDecodePacketRejectsWrongSSRC(t *testing.T) {
	raw, err := MarshalPacket(rtpItem(1, 0, 16), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := DecodePacket(raw, 3, 48000); err == nil {
		t.Fatal("wrong track SSRC must fail")
	} else if e, ok := merr.As(err); !ok || e.Kind != merr.KindInconsistent {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestDecodePacketRejectsGarbage(t *testing.T) {
	if _, _, _, err := DecodePacket([]byte{1, 2, 3}, 2, 48000); err == nil {
		t.Fatal("short packet must fail")
	}
	if _, _, _, err := DecodePacket([]byte{}, 2, 48000); err == nil {
		t.Fatal("empty packet must fail")
	}
}

func TestMarshalPacketRejectsEmptyPayload(t *testing.T) {
	if _, err := MarshalPacket(Item{Index: 1, TrackID: 2, Timestamp: 0}, false); err == nil {
		t.Fatal("empty payload must fail")
	}
}

func TestPacketizerFramesAndPreservesEssence(t *testing.T) {
	p, err := NewPipeline(Config{MaxBlocks: 4, MaxBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pz := NewPacketizer(p)
	it := rtpItem(1, 0, 64)
	if err := pz.Push(ctx, it, true); err != nil {
		t.Fatal(err)
	}
	if !pz.MarkerSent() {
		t.Fatal("expected marker")
	}
	got, err := p.Pop(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The payload is a framed packet whose essence round-trips intact, and the
	// byte bound counts the essence rather than the transport header.
	_, _, payload, err := DecodePacket(got.Payload, 2, 48000)
	if err != nil || len(payload) != 64 {
		t.Fatalf("decode framed: %v", err)
	}
	if got.Size() != 64 {
		t.Fatalf("essence size %d want 64", got.Size())
	}
	st := pz.Stats()
	if st.Packets != 1 || st.Bytes == 0 {
		t.Fatalf("stats %+v", st)
	}
}

func TestOversizedItemRejected(t *testing.T) {
	q, err := NewQueue(Config{MaxBlocks: 4, MaxBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	err = q.Push(context.Background(), item(1, 200, 0))
	if err == nil {
		t.Fatal("oversized item must be rejected, not block")
	}
	if e, ok := merr.As(err); !ok || e.Kind != merr.KindLimit {
		t.Fatalf("expected limit error, got %v", err)
	}
}
