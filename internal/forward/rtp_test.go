package forward

import (
	"encoding/binary"
	"testing"

	"github.com/pion/rtp"
)

func validPacket(seq uint16, ts uint32) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    0,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           0x42424242,
			Marker:         true,
		},
		Payload: []byte{0xde, 0xad, 0xbe, 0xef},
	}
}

// packetWithExtension crafts an RTP packet carrying a one-byte header
// extension directly on the wire, since pion/rtp keeps Extension fields
// unexported in v1.8.x.
func packetWithExtension(t *testing.T, seq uint16) []byte {
	t.Helper()
	base, err := validPacket(seq, 160).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	// Flip the extension bit in the first octet.
	base[0] |= 0x10
	ext := []byte{0xBE, 0xDE, 0x00, 0x01, 0x11, 0x00, 0x00, 0x00}
	out := make([]byte, 0, len(base)+len(ext))
	out = append(out, base[:12]...)
	out = append(out, ext...)
	out = append(out, base[12:]...)
	return out
}

func TestParseAcceptsValid(t *testing.T) {
	buf, err := validPacket(7, 160).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(buf)
	if err != nil {
		t.Fatalf("valid packet rejected: %v", err)
	}
	if !EqualMediaFields(validPacket(7, 160), got) {
		t.Fatal("media fields changed through parse")
	}

	extBuf := packetWithExtension(t, 8)
	extPkt, err := Parse(extBuf)
	if err != nil {
		t.Fatalf("valid extension packet rejected: %v", err)
	}
	if !extPkt.Header.Extension || len(extPkt.Header.Extensions) != 1 {
		t.Fatal("extension header not preserved")
	}
	// Re-marshal must reproduce the exact wire bytes: no silent rewriting.
	roundTrip, err := extPkt.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if string(roundTrip) != string(extBuf) {
		t.Fatal("extension wire bytes were rewritten by parse")
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	good, _ := validPacket(1, 0).Marshal()
	// Claim three CSRC identifiers while only providing the fixed header.
	claimsCSRC := make([]byte, 12)
	claimsCSRC[0] = 0x83 // V=2, CC=3
	cases := map[string][]byte{
		"short":        {0x80, 0x00},
		"bad version":  append([]byte{0x40}, good[1:]...),
		"missing csrc": claimsCSRC,
		"header only":  good[:11],
		"empty":        {},
	}
	for name, buf := range cases {
		if _, err := Parse(buf); err == nil {
			t.Errorf("case %s: expected validation failure", name)
		}
	}
}

func TestParseBadExtension(t *testing.T) {
	buf := packetWithExtension(t, 1)
	// Inflate the extension length word (offset 15) beyond the buffer.
	buf[15] = 0x0F
	if _, err := Parse(buf); err == nil {
		t.Fatal("inconsistent extension length must be rejected")
	}
}

func TestSeqDeltaWraparound(t *testing.T) {
	cases := []struct {
		a, b uint16
		want int32
	}{
		{10, 12, 2},
		{65534, 2, 4},
		{2, 65534, -4},
		{0, 0, 0},
		{32767, 0, -32767},
	}
	for _, c := range cases {
		if got := SeqDelta(c.a, c.b); got != c.want {
			t.Errorf("SeqDelta(%d,%d)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestTSDeltaWraparound(t *testing.T) {
	max := uint32(0xFFFFFFFF)
	if got := TSDelta(max-1, 2); got != 4 {
		t.Errorf("timestamp wrap delta = %d, want 4", got)
	}
	if got := TSDelta(100, 60); got != -40 {
		t.Errorf("backwards delta = %d, want -40", got)
	}
	if d := TSDelta(0, binary.BigEndian.Uint32([]byte{0, 0, 0, 0})); d != 0 {
		t.Errorf("zero delta = %d", d)
	}
}

func TestEqualMediaFields(t *testing.T) {
	a := validPacket(5, 100)
	b := validPacket(5, 100)
	if !EqualMediaFields(a, b) {
		t.Fatal("identical packets compare unequal")
	}
	b.Header.SSRC++
	if EqualMediaFields(a, b) {
		t.Fatal("SSRC change not detected")
	}
	if EqualMediaFields(a, nil) {
		t.Fatal("nil packet must compare unequal")
	}
}
