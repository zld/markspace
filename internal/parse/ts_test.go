package parse

import (
	"bytes"
	"testing"

	"mediarun/internal/fault"
)

// encodePTS encodes a 33-bit PTS into its 5-byte form.
func encodePTS(pts int64) []byte {
	v := uint64(pts) & 0x1ffffffff
	return []byte{
		0x20 | byte((v>>30)&0x07)<<1 | 1,
		byte(v >> 22),
		byte((v>>15)&0x7f)<<1 | 1,
		byte(v >> 7),
		byte(v&0x7f)<<1 | 1,
	}
}

// buildTS creates a TS segment with one PES packet carrying the given PTS
// followed by filler packets.
func buildTS(pts int64, filler int) []byte {
	pes := []byte{0x00, 0x00, 0x01, 0xe0, 0x00, 0x00, 0x80, 0x80, 0x05}
	pes = append(pes, encodePTS(pts)...)
	var buf bytes.Buffer
	// PES packet: PID 0x100, payload unit start set.
	pkt := make([]byte, TSPacketSize)
	pkt[0] = 0x47
	pkt[1] = 0x41 // PUSI + PID high
	pkt[2] = 0x00
	pkt[3] = 0x10 // payload only
	copy(pkt[4:], pes)
	buf.Write(pkt)
	for i := 0; i < filler; i++ {
		p := make([]byte, TSPacketSize)
		p[0] = 0x47
		p[1] = 0x01
		p[3] = 0x10
		buf.Write(p)
	}
	return buf.Bytes()
}

func TestParseTSGood(t *testing.T) {
	data := buildTS(126000, 9)
	info, err := ParseTS("seg0.ts", data)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasPTS || info.FirstPTS != 126000 || info.LastPTS != 126000 {
		t.Fatalf("bad PTS: %+v", info)
	}
	if info.Packets != 10 {
		t.Fatalf("packets = %d", info.Packets)
	}
	if info.PayloadBytes != 10*184 {
		t.Fatalf("payload = %d", info.PayloadBytes)
	}
}

func TestParseTSTruncated(t *testing.T) {
	data := buildTS(0, 2)[:187]
	_, err := ParseTS("seg0.ts", data)
	if got := faultStage(t, err); got != fault.StageMediaPayload {
		t.Fatalf("stage = %s", got)
	}
}

func TestParseTSLostSync(t *testing.T) {
	data := buildTS(0, 2)
	data[TSPacketSize] = 0x00 // corrupt sync byte of packet 2
	_, err := ParseTS("seg0.ts", data)
	if err == nil {
		t.Fatal("expected sync error")
	}
}

func TestParseTSEmpty(t *testing.T) {
	if _, err := ParseTS("s.ts", nil); err == nil {
		t.Fatal("expected error")
	}
}
