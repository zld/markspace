package ts

import (
	"testing"

	"github.com/example/tsrepair/internal/model"
)

func TestParseValidPacketWithPCR(t *testing.T) {
	raw := BuildPacket(BuildOpts{
		PID: 0x0100, PUSI: true, AdaptationControl: 3, CC: 7,
		WithPCR: true, PCRBase: 123456, PCRExt: 77,
		RandomAccess: true, Payload: []byte("pes-payload"),
	})
	p, err := Parse(raw, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.PID != 0x0100 || !p.PayloadUnitStart || p.ContinuityCounter != 7 {
		t.Fatalf("header fields wrong: %+v", p)
	}
	if p.Adaptation == nil || !p.Adaptation.HasPCR ||
		p.Adaptation.PCRBase != 123456 || p.Adaptation.PCRExtension != 77 {
		t.Fatalf("PCR parse wrong: %+v", p.Adaptation)
	}
	if !p.Adaptation.RandomAccess {
		t.Fatal("random access indicator lost")
	}
	if string(p.Payload[:len("pes-payload")]) != "pes-payload" {
		t.Fatalf("payload mismatch: %q", p.Payload)
	}
}

func TestParseRejectsBadSyncAndLength(t *testing.T) {
	raw := BuildPacket(BuildOpts{PID: 0x100})
	raw[0] = 0x00
	if _, err := Parse(raw, 0); !model.IsFatal(err) {
		t.Fatalf("bad sync must be fatal, got %v", err)
	}
	if _, err := Parse(raw[:100], 0); !model.IsFatal(err) {
		t.Fatalf("short buffer must be fatal, got %v", err)
	}
}

func TestParseRejectsAdaptationOverrun(t *testing.T) {
	raw := BuildPacket(BuildOpts{PID: 0x100, AdaptationControl: 2})
	raw[4] = 200 // adaptation length beyond packet
	if _, err := Parse(raw, 0); err == nil {
		t.Fatal("oversized adaptation field must be rejected")
	}
}

func TestSplitPacketsRoundtripAndTruncation(t *testing.T) {
	var stream []byte
	for i := 0; i < 5; i++ {
		stream = append(stream, BuildPacket(BuildOpts{
			PID: 0x200, CC: byte(i), Payload: []byte{byte(i)}})...)
	}
	stream = append(stream, 0x47, 0x01, 0x02) // truncated tail
	var count int
	tr, err := SplitPackets(stream, func(p *Packet) error {
		if p.ByteOffset != int64(count*PacketSize) {
			t.Fatalf("offset %d want %d", p.ByteOffset, count*PacketSize)
		}
		count++
		return nil
	})
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if count != 5 {
		t.Fatalf("packets=%d want 5", count)
	}
	if tr == nil || tr.Length != 3 || tr.Offset != 5*PacketSize {
		t.Fatalf("truncation range wrong: %+v", tr)
	}
}

func TestSplitPacketsResyncsAfterLeadingJunk(t *testing.T) {
	good := BuildPacket(BuildOpts{PID: 0x200})
	data := append([]byte{0x00, 0x11, 0x22}, good...)
	var count int
	if _, err := SplitPackets(data, func(*Packet) error { count++; return nil }); err != nil {
		t.Fatalf("resync: %v", err)
	}
	if count != 1 {
		t.Fatalf("packets=%d want 1", count)
	}
}

func TestRTPSpan(t *testing.T) {
	var tsData []byte
	for i := 0; i < 3; i++ {
		tsData = append(tsData, BuildPacket(BuildOpts{PID: 0x100, CC: byte(i)})...)
	}
	rtps, err := EncodeRTP(tsData, 0xDEADBEEF, 999, 100, 33)
	if err != nil || len(rtps) != 3 {
		t.Fatalf("encode: %v n=%d", err, len(rtps))
	}
	var gotSeq []uint16
	for _, raw := range rtps {
		if !LooksLikeRTP(raw) {
			t.Fatal("LooksLikeRTP rejected own packet")
		}
		env, err := ParseRTP(raw)
		if err != nil {
			t.Fatalf("parse rtp: %v", err)
		}
		if env.SSRC != 0xDEADBEEF || env.Timestamp != 999 || len(env.Packets) != 1 {
			t.Fatalf("envelope fields wrong: %+v", env)
		}
		gotSeq = append(gotSeq, env.SequenceNumber)
	}
	if gotSeq[0] != 100 || gotSeq[2] != 102 {
		t.Fatalf("sequence numbers wrong: %v", gotSeq)
	}
	if gap := RTPGap(102, 105); gap != 2 {
		t.Fatalf("gap=%d want 2", gap)
	}
}

func TestRejectsUnknownEncapsulation(t *testing.T) {
	junk := make([]byte, 300)
	if LooksLikeRTP(junk) {
		t.Fatal("zero buffer cannot look like RTP")
	}
	if _, err := ParseRTP(junk); !model.IsFatal(err) {
		t.Fatalf("unknown encapsulation must be fatal, got %v", err)
	}
}
