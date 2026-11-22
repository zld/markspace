package tsreader

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// makePacket builds a minimal 188-byte TS packet with the requested header
// flags. payload (if any) is copied after the 4-byte header and the remainder
// is stuffed with 0xFF.
func makePacket(pid uint16, cc uint8, pusi, tei bool, payload []byte) []byte {
	p := make([]byte, PacketSize)
	p[0] = SyncByte
	b1 := byte(0) // no transport priority, no scrambling
	if tei {
		b1 |= 0x80
	}
	if pusi {
		b1 |= 0x40
	}
	b1 |= byte((pid >> 8) & 0x1f)
	p[1] = b1
	p[2] = byte(pid & 0xff)
	// adaptation control: 0b01 payload only, 0b10 adaptation field only
	if len(payload) > 0 {
		p[3] = 0x10 | (cc & 0x0f)
	} else {
		p[3] = 0x20 | (cc & 0x0f)
	}
	off := 4
	if len(payload) > 0 {
		copy(p[off:], payload)
		off += len(payload)
	} else {
		p[off] = 0x01   // adaptation_field_length
		p[off+1] = 0x00 // flags: no PCR/discontinuity, rest is stuffing
		off += 2
	}
	for i := off; i < PacketSize; i++ {
		p[i] = 0xFF
	}
	return p
}

func TestReaderParsesHeaderFields(t *testing.T) {
	pkts := append([]byte{}, makePacket(256, 7, true, false, []byte{1, 2, 3})...)
	pkts = append(pkts, makePacket(257, 0, false, true, nil)...)

	r := NewReader(bytes.NewReader(pkts))
	got, err := r.Next()
	if err != nil {
		t.Fatalf("first packet: %v", err)
	}
	if got.Index != 0 || got.PID != 256 || got.ContinuityCounter != 7 {
		t.Fatalf("unexpected header: %+v", got)
	}
	if !got.PayloadUnitStart || got.TransportError || !got.HasPayload {
		t.Fatalf("unexpected flags: %+v", got)
	}
	if len(got.Raw) != PacketSize || got.Raw[0] != SyncByte {
		t.Fatalf("raw packet not retained correctly")
	}
	if !bytes.Equal(got.Payload[:3], []byte{1, 2, 3}) {
		t.Fatalf("payload mismatch: %x", got.Payload)
	}

	got, err = r.Next()
	if err != nil {
		t.Fatalf("second packet: %v", err)
	}
	if !got.TransportError || got.PayloadUnitStart {
		t.Fatalf("TEI/PUSI flags wrong: %+v", got)
	}
	if got.HasPayload {
		t.Fatalf("adaptation-only packet should have no payload")
	}

	if _, err = r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestReaderRejectsBadSyncAndTruncation(t *testing.T) {
	bad := makePacket(256, 0, false, false, nil)
	bad[0] = 0x00
	if _, err := NewReader(bytes.NewReader(bad)).Next(); err == nil {
		t.Fatal("expected sync byte error")
	}

	short := makePacket(256, 0, false, false, nil)[:100]
	if _, err := NewReader(bytes.NewReader(short)).Next(); err == nil {
		t.Fatal("expected truncated packet error")
	}
}
