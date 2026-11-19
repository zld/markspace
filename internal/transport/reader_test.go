package transport

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func packet(pid uint16, cc uint8, pusi bool, payload byte) []byte {
	b := make([]byte, PacketSize)
	b[0] = 0x47
	if pusi {
		b[1] = 0x40
	}
	b[1] |= byte(pid >> 8)
	b[2] = byte(pid)
	b[3] = 0x10 | cc&0x0f
	for i := 4; i < PacketSize; i++ {
		b[i] = payload
	}
	return b
}

func TestReaderParsesHeaderFields(t *testing.T) {
	raw := packet(0x0100, 5, true, 0xaa)
	r := NewReader(bytes.NewReader(raw))
	p, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if p.PID != 0x100 || p.CC != 5 || !p.PUSI || p.TEI || p.HasAF || !p.HasPay {
		t.Fatalf("unexpected header: %+v", p)
	}
	if len(p.Payload) != PacketSize-4 || p.Payload[0] != 0xaa {
		t.Fatalf("unexpected payload length %d", len(p.Payload))
	}
	if !bytes.Equal(p.Raw[:], raw) {
		t.Fatal("raw bytes not preserved")
	}
}

func TestReaderRejectsPartialTail(t *testing.T) {
	raw := append(packet(0x100, 1, false, 0), 0x47, 0x00, 0x00)
	r := NewReader(bytes.NewReader(raw))
	if _, err := r.Next(); err != nil {
		t.Fatal(err)
	}
	_, err := r.Next()
	var pt *PartialTailError
	if !errors.As(err, &pt) || !errors.Is(err, ErrPartialTail) {
		t.Fatalf("want PartialTailError, got %v", err)
	}
	if pt.Bytes != 3 {
		t.Fatalf("partial tail bytes = %d, want 3", pt.Bytes)
	}
}

func TestReaderRejectsBadSync(t *testing.T) {
	raw := make([]byte, PacketSize)
	r := NewReader(bytes.NewReader(raw))
	_, err := r.Next()
	var se *SyncError
	if !errors.As(err, &se) || !errors.Is(err, ErrBadSync) {
		t.Fatalf("want SyncError, got %v", err)
	}
}

func TestReaderAdaptationFieldOnlyAndDiscontinuity(t *testing.T) {
	raw := make([]byte, PacketSize)
	raw[0] = 0x47
	raw[1] = 0x01
	raw[2] = 0x23
	raw[3] = 0x20 // adaptation field only
	raw[4] = 183
	raw[5] = 0x80 // discontinuity_indicator
	r := NewReader(bytes.NewReader(raw))
	p, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if !p.AFOnly || !p.Discontinuity || p.HasPay || p.PID != 0x123 {
		t.Fatalf("unexpected AF packet: %+v", p)
	}
}

func TestReaderEmptyInputIsEOF(t *testing.T) {
	r := NewReader(bytes.NewReader(nil))
	if _, err := r.Next(); err != io.EOF {
		t.Fatalf("want io.EOF, got %v", err)
	}
}
