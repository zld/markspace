package ts

import "testing"

// encodePTS packs a 33-bit PTS into the 5-byte MPEG layout with marker bits.
// prefix carries the 4-bit PTS/DTS flag prefix (0x2x for PTS, 0x1x for DTS).
func encodePTS(prefix byte, pts int64) []byte {
	return []byte{
		prefix | byte(pts>>29)&0x0E | 0x01,
		byte(pts >> 22),
		byte(pts>>14)&0xFE | 0x01,
		byte(pts >> 7),
		byte(pts<<1)&0xFE | 0x01,
	}
}
// buildPES constructs a minimal video PES packet carrying PTS (and optional
// DTS).
func buildPES(pts int64, withDTS bool, payload []byte) []byte {
	flags := byte(0x80)
	hdr := encodePTS(0x20, pts) // '0010' prefix => PTS only
	hdrLen := 5
	if withDTS {
		flags = 0xC0
		hdr = append(hdr, encodePTS(0x10, pts-900)...) // '0001' prefix => DTS
		hdrLen = 10
	}
	// Standard PES header: start code (0-2), stream id (3), length (4-5),
	// marker/scrambling byte (6=0x80), PTS/DTS flags (7), header length (8).
	b := []byte{0x00, 0x00, 0x01, 0xE0, 0x00, 0x00, 0x80, flags, byte(hdrLen)}
	b = append(b, hdr...)
	b = append(b, payload...)
	// PES_packet_length covers bytes after the length field.
	n := len(b) - 6
	b[4] = byte(n >> 8)
	b[5] = byte(n)
	return b
}

func TestParsePESPTSOnly(t *testing.T) {
	pes := buildPES(90_000, false, []byte{0xAA, 0xBB})
	got := parsePES(pes, 188, false)
	if !got.HasPTS || got.PTS != 90_000 || got.HasDTS {
		t.Fatalf("got %+v", got)
	}
	if got.ByteOffset != 188 || len(got.Payload) != 2 || got.StreamID != 0xE0 {
		t.Fatalf("fields wrong: %+v", got)
	}
}

func TestParsePESPTSAndDTS(t *testing.T) {
	pes := buildPES(180_000, true, []byte{1})
	got := parsePES(pes, 0, false)
	if !got.HasPTS || !got.HasDTS || got.PTS != 180_000 || got.DTS != 179_100 {
		t.Fatalf("got %+v", got)
	}
}

func TestParsePESMalformedHeader(t *testing.T) {
	got := parsePES([]byte{0, 0, 1, 0xE0}, 0, false)
	if got.HasPTS || got.HasDTS || got.StreamID != 0 {
		t.Fatal("malformed PES must not invent timing or identity")
	}
}

func TestPESAssembledAcrossTSPackets(t *testing.T) {
	d := NewPESDemuxer(0x100)
	pes := buildPES(45_000, false, make([]byte, 300))
	first := &Packet{PID: 0x100, PUSI: true, RandomAccess: true,
		AdaptationControl: AFCPayloadOnly, AdaptationLength: -1,
		Offset: 0, Payload: pes[:100]}
	second := &Packet{PID: 0x100, PUSI: false,
		AdaptationControl: AFCPayloadOnly, AdaptationLength: -1,
		Offset: 188, Payload: pes[100:]}
	if d.HandlePacket(first) != nil {
		t.Fatal("first packet must not complete a PES")
	}
	if d.HandlePacket(second) != nil {
		t.Fatal("PES completes only on next PUSI or Flush")
	}
	got := d.Flush(0x100)
	if got == nil || !got.HasPTS || got.PTS != 45_000 || !got.Keyframe {
		t.Fatalf("got %+v", got)
	}
}

func TestPESCompletesOnNextPUSI(t *testing.T) {
	d := NewPESDemuxer(0x100)
	p1 := buildPES(1000, false, []byte{1, 2})
	p2 := buildPES(2000, false, []byte{3, 4})
	got := d.HandlePacket(&Packet{PID: 0x100, PUSI: true,
		AdaptationControl: AFCPayloadOnly, AdaptationLength: -1, Payload: p1})
	if got != nil {
		t.Fatal("first PUSI has nothing to flush")
	}
	got = d.HandlePacket(&Packet{PID: 0x100, PUSI: true,
		AdaptationControl: AFCPayloadOnly, AdaptationLength: -1, Payload: p2})
	if got == nil || got.PTS != 1000 {
		t.Fatalf("expected previous PES (PTS 1000), got %+v", got)
	}
	got = d.Flush(0x100)
	if got == nil || got.PTS != 2000 {
		t.Fatalf("flush must return second PES, got %+v", got)
	}
}

func TestIDRNALDetection(t *testing.T) {
	// Annex-B start code 00 00 01 followed by NAL type 5 (IDR).
	payload := []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x11, 0x22}
	if !hasIDRNAL(payload) {
		t.Error("IDR NAL not detected")
	}
	nonIDR := []byte{0x00, 0x00, 0x00, 0x01, 0x61, 0x33}
	if hasIDRNAL(nonIDR) {
		t.Error("non-IDR NAL falsely detected")
	}
}
