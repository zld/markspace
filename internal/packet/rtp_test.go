package packet

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/pion/rtcp"

	"mediaforge/internal/mediaerr"
)

// buildRTP assembles an RTP packet with optional CSRCs, a 0xBEDE extension
// and padding. Payload bytes are copied verbatim.
func buildRTP(seq uint16, ts uint32, csrc []uint32, ext map[uint8][]byte, pad int, payload []byte) []byte {
	b := make([]byte, 12)
	b[0] = 0x80 | byte(len(csrc))
	binary.BigEndian.PutUint16(b[2:], seq)
	binary.BigEndian.PutUint32(b[4:], ts)
	binary.BigEndian.PutUint32(b[8:], 0xDEADBEEF)
	extensions := len(ext) > 0
	if extensions {
		b[0] |= 0x10
	}
	for _, c := range csrc {
		var x [4]byte
		binary.BigEndian.PutUint32(x[:], c)
		b = append(b, x[:]...)
	}
	if extensions {
		var extBody []byte
		for id, data := range ext {
			extBody = append(extBody, id<<4|byte(len(data)-1))
			extBody = append(extBody, data...)
		}
		for len(extBody)%4 != 0 {
			extBody = append(extBody, 0)
		}
		hdr := make([]byte, 4)
		binary.BigEndian.PutUint16(hdr, 0xBEDE)
		binary.BigEndian.PutUint16(hdr[2:], uint16(len(extBody)/4))
		b = append(b, hdr...)
		b = append(b, extBody...)
	}
	if pad > 0 {
		b[0] |= 0x20
	}
	b = append(b, payload...)
	for i := 0; i < pad-1; i++ {
		b = append(b, 0)
	}
	if pad > 0 {
		b = append(b, byte(pad))
	}
	return b
}

func TestParsePacketRejectsBadVersion(t *testing.T) {
	raw := buildRTP(1, 0, nil, nil, 0, []byte{1, 2, 3})
	raw[0] = (raw[0] &^ 0xC0) | (1 << 6)
	_, err := ParsePacket(raw, 100)
	if !mediaerr.Is(err, mediaerr.CodeBadPacket) {
		t.Fatalf("err=%v", err)
	}
	var me *mediaerr.Error
	if !errors.As(err, &me) || !me.HasOffset || me.Offset != 100 || !me.HasSeq || me.Seq != 1 {
		t.Fatalf("missing context: %+v", me)
	}
}

func TestParsePacketCSRCAndExtension(t *testing.T) {
	payload := []byte{9, 8, 7, 6}
	raw := buildRTP(7, 960, []uint32{1, 2, 3}, map[uint8][]byte{2: {0xaa, 0xbb}}, 4, payload)
	p, err := ParsePacket(raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.CSRC) != 3 || p.CSRC[2] != 3 {
		t.Fatalf("csrc=%v", p.CSRC)
	}
	if p.ExtensionProfile != 0xBEDE {
		t.Fatalf("profile=%x", p.ExtensionProfile)
	}
	if p.PaddingSize != 4 {
		t.Fatalf("padding=%d", p.PaddingSize)
	}
	if !bytes.Equal(p.Payload, payload) {
		t.Fatalf("payload=%v", p.Payload)
	}
}

func TestParsePacketCSRCTruncated(t *testing.T) {
	raw := buildRTP(3, 0, nil, nil, 0, []byte{1})
	raw[0] |= 4 // declare four CSRCs, none present
	_, err := ParsePacket(raw, 0)
	if !mediaerr.Is(err, mediaerr.CodeBadPacket) {
		t.Fatalf("err=%v", err)
	}
}

func TestParsePacketExtensionOverrun(t *testing.T) {
	raw := buildRTP(4, 0, nil, nil, 0, []byte{1, 2})
	raw[0] |= 0x10
	// Rewrite header: extension flag set but extension words claim 0xffff.
	raw = append(raw[:12], []byte{0xBE, 0xDE, 0xFF, 0xFF}...)
	_, err := ParsePacket(raw, 0)
	if mediaerr.CodeOf(err) != mediaerr.CodeBadPacket {
		t.Fatalf("err=%v", err)
	}
}

func TestParsePacketPaddingValidation(t *testing.T) {
	raw := buildRTP(5, 0, nil, nil, 0, []byte{1, 2})
	raw[0] |= 0x20
	raw = append(raw, 0x7f)
	_, err := ParsePacket(raw, 0)
	if mediaerr.CodeOf(err) != mediaerr.CodeBadPacket {
		t.Fatalf("err=%v", err)
	}
}

func TestParsePacketPayloadCRC(t *testing.T) {
	payload := []byte{4, 5, 6, 7}
	crc := make([]byte, 4)
	binary.BigEndian.PutUint32(crc, FrameCRC(payload))
	good := buildRTP(9, 0, nil, map[uint8][]byte{ExtensionCRC: crc}, 0, payload)
	if _, err := ParsePacket(good, 0); err != nil {
		t.Fatalf("good crc rejected: %v", err)
	}
	good[len(good)-1] ^= 0xff
	p, err := ParsePacket(good, 0)
	if mediaerr.CodeOf(err) != mediaerr.CodeCorrupt {
		t.Fatalf("err=%v", err)
	}
	if p == nil {
		t.Fatal("expected partially parsed packet with corrupt error")
	}
}

func TestReadStreamContinuesAfterBadPacket(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	good := buildRTP(1, 0, nil, nil, 0, []byte{1})
	bad := buildRTP(2, 0, nil, nil, 0, []byte{2})
	bad[0] = (bad[0] &^ 0xC0) | (3 << 6)
	good2 := buildRTP(3, 0, nil, nil, 0, []byte{3})
	for _, rec := range []struct {
		kind RecordKind
		body []byte
	}{{KindRTP, good}, {KindRTP, bad}, {KindRTP, good2}} {
		if err := w.WriteRecord(rec.kind, rec.body); err != nil {
			t.Fatal(err)
		}
	}
	s, err := ReadStream(&buf, MaxInputSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Records) != 3 {
		t.Fatalf("records=%d", len(s.Records))
	}
	if s.Records[0].Err != nil || s.Records[2].Err != nil {
		t.Fatal("good packets rejected")
	}
	if mediaerr.CodeOf(s.Records[1].Err) != mediaerr.CodeBadPacket {
		t.Fatalf("bad packet err=%v", s.Records[1].Err)
	}
}

func TestReadStreamRejectsOversizedRecord(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString(Magic)
	hdr := make([]byte, 5)
	binary.BigEndian.PutUint32(hdr, MaxPacketSize+1)
	hdr[4] = byte(KindRTP)
	buf.Write(hdr)
	_, err := ReadStream(&buf, MaxInputSize)
	if mediaerr.CodeOf(err) != mediaerr.CodeBadPacket {
		t.Fatalf("err=%v", err)
	}
}

func TestReadStreamTruncatedRecord(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString(Magic)
	hdr := make([]byte, 5)
	binary.BigEndian.PutUint32(hdr, 100)
	hdr[4] = byte(KindRTP)
	buf.Write(hdr)
	buf.Write([]byte{1, 2, 3})
	s, err := ReadStream(&buf, MaxInputSize)
	if err != nil {
		t.Fatalf("truncated tail must be recoverable: %v", err)
	}
	if len(s.Records) != 1 || mediaerr.CodeOf(s.Records[0].Err) != mediaerr.CodeTruncated {
		t.Fatalf("records=%+v", s.Records)
	}
	if s.Records[0].DeclaredLen != 100 {
		t.Fatalf("declared=%d", s.Records[0].DeclaredLen)
	}
}

func TestParseReportsRR(t *testing.T) {
	src := rtcp.ReceiverReport{
		SSRC: 42,
		Reports: []rtcp.ReceptionReport{{
			SSRC: 7, FractionLost: 12, TotalLost: 9,
			LastSequenceNumber: 0x00010005, Jitter: 33,
		}},
	}
	raw, err := src.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	out, err := ParseReports(raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Receivers) != 1 || len(out.Receivers[0].Reports) != 1 {
		t.Fatalf("reports=%+v", out)
	}
	b := out.Receivers[0].Reports[0]
	if b.TotalLost != 9 || b.HighestSeq != 0x00010005 || b.Jitter != 33 {
		t.Fatalf("block=%+v", b)
	}
	if out.DeclaredLoss() != 9 {
		t.Fatalf("loss=%d", out.DeclaredLoss())
	}
}

func TestParseReportsCompundBadTail(t *testing.T) {
	raw, err := rtcp.ReceiverReport{SSRC: 1}.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, 0x80, 0xC8, 0x00, 0x7f)
	out, err := ParseReports(raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Errors) == 0 {
		t.Fatal("expected a subpacket error")
	}
}
