package psi

import (
	"testing"

	"tsrebuild/internal/tsreader"
)

// splitPayload wraps a byte stream into TS-style payloads: the first packet
// carries PUSI and a zero pointer_field, the rest are continuation packets.
func splitPayload(pid uint16, data []byte, chunk int) []*tsreader.Packet {
	var out []*tsreader.Packet
	off, idx := 0, int64(0)
	first := true
	for off < len(data) || first {
		pkt := &tsreader.Packet{PID: pid, Index: idx, HasPayload: true}
		room := chunk
		if first {
			pkt.PayloadUnitStart = true
			pkt.Payload = []byte{0x00}
			room--
		}
		n := len(data) - off
		if n > room {
			n = room
		}
		pkt.Payload = append(pkt.Payload, data[off:off+n]...)
		off += n
		out = append(out, pkt)
		first = false
		idx++
		if n == 0 {
			break
		}
	}
	return out
}

func TestAssemblerCrossPacketSection(t *testing.T) {
	section := make([]byte, 300)
	section[0] = 0x00                                          // table_id PAT
	section[1], section[2] = 0xB0|byte(297>>8), byte(297&0xff) // syntax indicator + length 297 (0x129)

	pkts := splitPayload(0, section, 50)
	a := NewAssembler()
	var got []*Section
	for _, p := range pkts {
		secs, err := a.Push(p)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, secs...)
	}
	if len(got) != 1 || len(got[0].Data) != 300 {
		t.Fatalf("expected one 300-byte section, got %d", len(got))
	}
	if got[0].PID != 0 || got[0].StartPacket != 0 || got[0].EndPacket != int64(len(pkts)-1) {
		t.Fatalf("section bookkeeping wrong: %+v", got[0])
	}
}

func TestAssemblerMultipleSectionsInOnePacket(t *testing.T) {
	s1 := []byte{0x00, 0xB0, 0x05, 1, 2, 3, 4, 5} // length 5 => 8 bytes total
	s2 := []byte{0x02, 0xB0, 0x05, 6, 7, 8, 9, 10}
	payload := append(append([]byte{0x00}, s1...), s2...)
	a := NewAssembler()
	secs, err := a.Push(&tsreader.Packet{
		PID: 0, Index: 0, HasPayload: true, PayloadUnitStart: true, Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(secs) != 2 || string(secs[0].Data) != string(s1) || string(secs[1].Data) != string(s2) {
		t.Fatalf("expected two sections, got %+v", secs)
	}
}

func TestAssemblerPointerFieldAndStuffing(t *testing.T) {
	s := []byte{0x00, 0xB0, 0x03, 0xaa, 0xbb, 0xcc} // 6 bytes
	// pointer_field=2 means the section starts at byte 3; bytes 1-2 are stuffing.
	payload := append([]byte{0x02, 0xff, 0xff}, s...)
	a := NewAssembler()
	secs, err := a.Push(&tsreader.Packet{
		PID: 0, Index: 0, HasPayload: true, PayloadUnitStart: true, Payload: payload,
	})
	if err != nil || len(secs) != 1 || string(secs[0].Data) != string(s) {
		t.Fatalf("pointer handling wrong: err=%v secs=%v", err, secs)
	}
}

func TestAssemblerTEIDropsPartialSection(t *testing.T) {
	section := make([]byte, 100)
	section[0], section[1], section[2] = 0x00, 0xB0, 97
	pkts := splitPayload(0, section, 40)
	a := NewAssembler()
	if _, err := a.Push(pkts[0]); err != nil {
		t.Fatal(err)
	}
	// Corrupt continuation packet must invalidate the partial section.
	bad := &tsreader.Packet{PID: 0, Index: 1, HasPayload: true, TransportError: true}
	if _, err := a.Push(bad); err != nil {
		t.Fatal(err)
	}
	rest, err := a.Push(pkts[2])
	if err != nil || len(rest) != 0 {
		t.Fatalf("no section should complete after TEI reset, got %v %v", rest, err)
	}
	if st := a.Stats(0); st.TEIDrops != 1 {
		t.Fatalf("TEI drop not counted: %+v", st)
	}
}

func TestAssemblerTruncatedSectionIsReplaced(t *testing.T) {
	first := []byte{0x00, 0xB0, 0x10, 1, 2} // claims 19 bytes, only 2 arrive
	a := NewAssembler()
	if _, err := a.Push(&tsreader.Packet{
		PID: 0, Index: 0, HasPayload: true, PayloadUnitStart: true,
		Payload: append([]byte{0}, first...),
	}); err != nil {
		t.Fatal(err)
	}
	complete := []byte{0x00, 0xB0, 0x03, 7, 8, 9}
	secs, err := a.Push(&tsreader.Packet{
		PID: 0, Index: 1, HasPayload: true, PayloadUnitStart: true,
		Payload: append([]byte{0}, complete...),
	})
	if err != nil || len(secs) != 1 || len(secs[0].Data) != 6 {
		t.Fatalf("truncated section should be replaced: err=%v secs=%v", err, secs)
	}
	if a.Stats(0).TruncatedDrops != 1 {
		t.Fatal("truncated drop not counted")
	}
}

func TestCRCTableSelfConsistent(t *testing.T) {
	// Standard CRC-32/MPEG-2 check value for the ASCII string "123456789".
	got := crc32MPEG2([]byte("123456789"))
	if got != 0x0376e6e7 {
		t.Fatalf("crc32MPEG2 check value wrong: %08x", got)
	}
	if verifyCRC(append([]byte("123456789"), 0x03, 0x76, 0xe6, 0xe7)) != true {
		t.Fatal("verifyCRC should accept a section ending in its valid CRC")
	}
}
