package psi

import (
	"bytes"
	"errors"
	"testing"
)

func TestAssemblerPointerAndMultipleSections(t *testing.T) {
	s1 := buildSection(TableIDPAT, 7, 3, true, 0, 1, []byte{0, 1, 0xe1, 0x00})
	s2 := buildSection(TableIDPAT, 7, 3, true, 1, 1, []byte{0, 2, 0xe2, 0x00})
	prefix := []byte{0xaa, 0xbb}
	payload := append([]byte{byte(len(prefix))}, prefix...)
	payload = append(payload, s1...)
	payload = append(payload, s2...)
	payload = append(payload, 0xff)

	a := NewAssembler()
	got, errs := a.Feed(payload, true)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sections, want 2", len(got))
	}
	if bytes.Equal(got[0].Body, s1[8:len(s1)-4]) == false || got[1].SectionNumber != 1 {
		t.Fatalf("unexpected section contents: %+v %+v", got[0], got[1])
	}
	if got[0].Version != 3 || !got[0].CurrentNext || got[0].Extension != 7 {
		t.Fatalf("syntax header mismatch: %+v", got[0])
	}
}

func TestAssemblerCrossPacketSection(t *testing.T) {
	body := bytes.Repeat([]byte{0x42}, 300)
	s := buildSection(TableIDPMT, 101, 1, true, 0, 0, body)
	a := NewAssembler()
	first := a
	var got []*Section
	var errs []error
	chunk := s[:100]
	sec, e := first.Feed(append([]byte{0}, chunk...), true)
	got = append(got, sec...)
	errs = append(errs, e...)
	if len(got) != 0 || len(errs) != 0 || !first.Pending() {
		t.Fatalf("section should still be pending: got=%d errs=%v pending=%v", len(got), errs, first.Pending())
	}
	rest := s[100:]
	for len(rest) > 0 {
		n := 120
		if n > len(rest) {
			n = len(rest)
		}
		sec, e = first.Feed(rest[:n], false)
		got = append(got, sec...)
		errs = append(errs, e...)
		rest = rest[n:]
	}
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(got) != 1 || got[0].TableID != TableIDPMT || len(got[0].Body) != len(body) {
		t.Fatalf("cross-packet section not recovered: %+v", got)
	}
}

func TestAssemblerCRCFailureDropsSection(t *testing.T) {
	s := buildSection(TableIDPAT, 1, 0, true, 0, 0, []byte{0, 1, 0xe1, 0x00})
	s[len(s)-1] ^= 0xff
	a := NewAssembler()
	got, errs := a.Feed(append([]byte{0}, s...), true)
	if len(got) != 0 {
		t.Fatalf("bad CRC section was accepted: %+v", got)
	}
	if len(errs) != 1 || !errors.Is(errs[0], ErrSectionCRC) {
		t.Fatalf("want exactly one CRC error, got %v", errs)
	}
}

func TestAssemblerRejectsOversizedLength(t *testing.T) {
	payload := []byte{0, 0x00, 0xbf, 0xff}
	a := NewAssembler()
	got, errs := a.Feed(payload, true)
	if len(got) != 0 || len(errs) != 1 || !errors.Is(errs[0], ErrSectionLength) {
		t.Fatalf("oversized section not rejected: got=%v errs=%v", got, errs)
	}
	if a.Pending() {
		t.Fatal("assembler retained state after length error")
	}
}

func TestAssemblerPointerContinuesPendingSection(t *testing.T) {
	// Exercise pointer_field pointing past continuation bytes of a section
	// that started in the previous packet.
	body := bytes.Repeat([]byte{0x11}, 200)
	s := buildSection(TableIDPMT, 9, 2, true, 0, 0, body)
	a := NewAssembler()
	if _, errs := a.Feed(append([]byte{0}, s[:50]...), true); len(errs) != 0 {
		t.Fatal(errs)
	}
	rest := s[50:]
	tail := []byte{0xff, 0xff, 0xff, 0xff}
	payload := append([]byte{byte(len(rest))}, rest...)
	payload = append(payload, tail...)
	got, errs := a.Feed(payload, true)
	if len(errs) != 0 || len(got) != 1 {
		t.Fatalf("continuation via pointer_field failed: got=%d errs=%v", len(got), errs)
	}
}

func TestAssemblerDropsPendingOnExplicitReset(t *testing.T) {
	body := bytes.Repeat([]byte{0x22}, 200)
	s := buildSection(TableIDPMT, 3, 0, true, 0, 0, body)
	a := NewAssembler()
	if _, errs := a.Feed(append([]byte{0}, s[:40]...), true); len(errs) != 0 {
		t.Fatal(errs)
	}
	a.Reset()
	got, errs := a.Feed(s[40:], false)
	if len(got) != 0 || len(errs) != 0 {
		t.Fatalf("continuation after reset should be ignored: got=%d errs=%v", len(got), errs)
	}
}
