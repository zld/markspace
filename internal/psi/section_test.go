package psi

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
)

// The canonical PAT section used by the go-astits test suite. It carries the
// MPEG-2 PSI CRC and therefore doubles as a cross-check of our CRC code.
const patHex = "00b00d0001c100000001f0002ab104b2"

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCRC32MatchesKnownSection(t *testing.T) {
	raw := mustHex(t, patHex)
	if CRC32(raw) != 0 {
		t.Fatalf("CRC32 of valid section = %#x, want 0", CRC32(raw))
	}
	bad := append([]byte(nil), raw...)
	bad[9] ^= 0xff
	if CRC32(bad) == 0 {
		t.Fatal("corrupted section still validates")
	}
}

func TestParseSectionFields(t *testing.T) {
	s, err := ParseSection(mustHex(t, patHex))
	if err != nil {
		t.Fatal(err)
	}
	if s.TableID != 0 || s.Extension != 1 || s.Version != 0 || !s.CurrentNext {
		t.Fatalf("unexpected fields: %+v", s)
	}
	if !bytes.Equal(s.Body, mustHex(t, "0001f000")) {
		t.Fatalf("body = %x", s.Body)
	}
}

func TestAssemblerCrossPacketAndPointer(t *testing.T) {
	raw := mustHex(t, patHex)
	// First payload: PUSI with pointer 0, only the first 6 bytes.
	a := NewAssembler()
	got, err := a.Push(append([]byte{0x00}, raw[:6]...), true)
	if err != nil || len(got) != 0 {
		t.Fatalf("first push: %v %v", got, err)
	}
	// Second payload carries the remainder plus 0xff stuffing.
	got, err = a.Push(append(append([]byte(nil), raw[6:]...), 0xff, 0xff), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TableID != 0 {
		t.Fatalf("got %d sections", len(got))
	}
	// A new PUSI with a non-zero pointer completes a pending section first.
	a = NewAssembler()
	if _, err = a.Push(append([]byte{0x00}, raw[:10]...), true); err != nil {
		t.Fatal(err)
	}
	got, err = a.Push(append([]byte{byte(len(raw) - 10)}, append(raw[10:], 0xff)...), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("pointer completion: got %d sections", len(got))
	}
}

func TestAssemblerDiscardsBadCRCAndContinues(t *testing.T) {
	raw := mustHex(t, patHex)
	bad := append([]byte(nil), raw...)
	bad[len(bad)-1] ^= 0x01
	a := NewAssembler()
	got, err := a.Push(append([]byte{0x00}, bad...), true)
	if err != nil {
		t.Fatalf("bad CRC should be counted, not fatal: %v", err)
	}
	if len(got) != 0 || a.Stats().CRCErrors != 1 {
		t.Fatalf("got %d sections, crc errors %d", len(got), a.Stats().CRCErrors)
	}
	got, err = a.Push(append([]byte{0x00}, raw...), true)
	if err != nil || len(got) != 1 {
		t.Fatalf("good section after bad: %v %v", got, err)
	}
}

func TestAssemblerMultiSection(t *testing.T) {
	raw := mustHex(t, patHex)
	payload := append([]byte{0x00}, raw...)
	payload = append(payload, raw...)
	payload = append(payload, 0xff)
	a := NewAssembler()
	got, err := a.Push(payload, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sections, want 2", len(got))
	}
}

func TestAssemblerRejectsOverlongSection(t *testing.T) {
	a := NewAssembler()
	hdr := []byte{0x00, 0xb0 | 0x0f, 0xff} // section_length 4095 > limit
	_, err := a.Push(append([]byte{0x00}, hdr...), true)
	if !errors.Is(err, ErrInvalidSection) {
		t.Fatalf("got %v, want ErrInvalidSection", err)
	}
	if a.Stats().LengthErrors != 1 {
		t.Fatalf("length errors = %d", a.Stats().LengthErrors)
	}
}
