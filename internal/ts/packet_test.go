package ts

import (
	"bytes"
	"testing"
)

func mkPacket(pid uint16, pusi bool, cc uint8, payload byte) []byte {
	p := make([]byte, PacketSize)
	p[0] = SyncByte
	if pusi {
		p[1] = 0x40
	}
	p[1] |= byte(pid >> 8)
	p[2] = byte(pid)
	p[3] = 0x10 | (cc & 0x0f) // payload only
	p[4] = payload
	return p
}

func TestScannerHappyPath(t *testing.T) {
	var in bytes.Buffer
	for i := 0; i < 5; i++ {
		in.Write(mkPacket(0x100, i == 0, uint8(i), byte(i)))
	}
	s := NewScanner(&in)
	n := 0
	for p := s.Next(); p != nil; p = s.Next() {
		if p.PID != 0x100 {
			t.Fatalf("pid: %#x", p.PID)
		}
		n++
	}
	if n != 5 {
		t.Fatalf("packets: %d", n)
	}
	if len(s.Events()) != 0 {
		t.Fatalf("unexpected events: %+v", s.Events())
	}
}

func TestScannerResync(t *testing.T) {
	var in bytes.Buffer
	// Garbage containing a false 0x47 that begins a structurally invalid
	// candidate: PID 0x1FFF is reserved for null packets, but the candidate
	// also claims a payload, so ParsePacket rejects it.
	in.Write([]byte{0x00, 0x11, 0x47, 0x1f, 0xff, 0x10, 0x07})
	for len(in.Bytes())%PacketSize != 4 {
		in.WriteByte(0x00)
	}
	good := mkPacket(0x200, true, 0, 7)
	in.Write(good)
	s := NewScanner(&in)
	p := s.Next()
	if p == nil || p.PID != 0x200 {
		t.Fatalf("lost the good packet: %v", p)
	}
	var resyncs int
	for _, e := range s.Events() {
		if e.Kind == EventResync {
			resyncs++
		}
	}
	if resyncs == 0 {
		t.Fatalf("expected a resync event, got %+v", s.Events())
	}
}

func TestScannerTruncated(t *testing.T) {
	in := bytes.NewReader(append(mkPacket(0x100, false, 0, 0), 0x47, 0x01))
	s := NewScanner(in)
	p := s.Next()
	if p == nil {
		t.Fatal("first packet lost")
	}
	if p = s.Next(); p != nil {
		t.Fatal("trailer must not yield a packet")
	}
	found := false
	for _, e := range s.Events() {
		if e.Kind == EventTruncated {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing truncated event: %+v", s.Events())
	}
}

func TestContinuityCheck(t *testing.T) {
	var in bytes.Buffer
	in.Write(mkPacket(0x300, false, 0, 1))
	in.Write(mkPacket(0x300, false, 2, 2)) // skips CC 1
	in.Write(mkPacket(0x300, false, 3, 3))
	s := NewScanner(&in)
	for s.Next() != nil {
	}
	var cc []Event
	for _, e := range s.Events() {
		if e.Kind == EventCCError {
			cc = append(cc, e)
		}
	}
	if len(cc) != 1 || cc[0].PID != 0x300 || cc[0].WantCC != 1 || cc[0].GotCC != 2 {
		t.Fatalf("cc events: %+v", cc)
	}
}

func TestParsePacketErrors(t *testing.T) {
	if _, err := ParsePacket(make([]byte, 10)); err == nil {
		t.Fatal("short packet must error")
	}
	bad := mkPacket(0x100, false, 0, 0)
	bad[0] = 0x00
	if _, err := ParsePacket(bad); err == nil {
		t.Fatal("bad sync must error")
	}
}

func TestPayloadAndAdaptation(t *testing.T) {
	p := mkPacket(0x100, false, 0, 0xAB)
	pkt, err := ParsePacket(p)
	if err != nil {
		t.Fatal(err)
	}
	pl := pkt.Payload()
	if len(pl) != 184 || pl[0] != 0xAB {
		t.Fatalf("payload: %x", pl)
	}
	// Adaptation-only packet has no payload.
	adapt := make([]byte, PacketSize)
	adapt[0] = SyncByte
	adapt[3] = 0x20
	adapt[4] = 7
	ap, err := ParsePacket(adapt)
	if err != nil {
		t.Fatal(err)
	}
	if ap.HasPayload || ap.Payload() != nil {
		t.Fatal("adaptation-only packet must have no payload")
	}
}
