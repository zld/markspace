package psi

import (
	"errors"
	"testing"
)

func TestAssemblerRejectsPointerPastPayload(t *testing.T) {
	a := NewAssembler()
	got, errs := a.Feed([]byte{200, 0x00, 0xb0}, true)
	if len(got) != 0 || len(errs) != 1 || !errors.Is(errs[0], ErrSectionTruncated) {
		t.Fatalf("pointer overflow not rejected: got=%d errs=%v", len(got), errs)
	}
	if a.Pending() {
		t.Fatal("assembler kept state after pointer overflow")
	}
}

func TestAssemblerRejectsTinySectionLength(t *testing.T) {
	// section_length 4 cannot hold the PSI syntax header plus CRC.
	a := NewAssembler()
	got, errs := a.Feed([]byte{0, 0x00, 0xb0, 0x04, 0x00}, true)
	if len(got) != 0 || len(errs) != 1 || !errors.Is(errs[0], ErrSectionLength) {
		t.Fatalf("tiny section not rejected: got=%d errs=%v", len(got), errs)
	}
}

func TestAssemblerAcceptsMaximumLength(t *testing.T) {
	body := make([]byte, MaxSectionLength-SyntaxSize-CRCSize)
	s := buildSection(TableIDPMT, 1, 0, true, 0, 0, body)
	if len(s) != HeaderSize+MaxSectionLength {
		t.Fatalf("section size %d, want %d", len(s), HeaderSize+MaxSectionLength)
	}
	a := NewAssembler()
	var got []*Section
	for i := 0; i < len(s); i += 184 {
		end := i + 184
		if end > len(s) {
			end = len(s)
		}
		pusi := i == 0
		var payload []byte
		if pusi {
			payload = append([]byte{0}, s[i:end]...)
		} else {
			payload = s[i:end]
		}
		secs, errs := a.Feed(payload, pusi)
		if len(errs) != 0 {
			t.Fatalf("feed %d: %v", i, errs)
		}
		got = append(got, secs...)
	}
	if len(got) != 1 || len(got[0].Body) != len(body) {
		t.Fatalf("maximum-length section not reassembled: %+v", got)
	}
}

func TestParseSectionRejectsNonSyntaxPAT(t *testing.T) {
	raw := []byte{TableIDPAT, 0x30, 0x05, 0, 0, 0, 0, 0}
	_, err := ParseSection(raw)
	if !errors.Is(err, ErrSectionSyntax) {
		t.Fatalf("want ErrSectionSyntax, got %v", err)
	}
}

func TestContinuationPacketCannotStartNewSection(t *testing.T) {
	// A section ending mid-packet without pointer_field: leftover bytes must
	// not be interpreted as another section, even if they look like a valid
	// header with a CRC-bearing body.
	s := buildSection(TableIDPAT, 1, 0, true, 0, 0, []byte{0, 1, 0xe1, 0x00})
	a := NewAssembler()
	half := len(s) / 2
	if _, errs := a.Feed(append([]byte{0}, s[:half]...), true); len(errs) != 0 {
		t.Fatal(errs)
	}
	// Follow with the rest of this section plus a whole second section.
	payload := append(append([]byte{}, s[half:]...), s...)
	got, errs := a.Feed(payload, false)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(got) != 1 {
		t.Fatalf("continuation packet yielded %d sections, want 1", len(got))
	}
	if a.Pending() {
		t.Fatal("stuffing created pending state")
	}
}
