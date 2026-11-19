package psi

import (
	"bytes"
	"errors"
	"testing"
)

func patSection(version uint8, currentNext bool, programs ...ProgramEntry) []byte {
	var body []byte
	for _, p := range programs {
		body = append(body, byte(p.ProgramNumber>>8), byte(p.ProgramNumber), 0xe0|byte(p.PID>>8), byte(p.PID))
	}
	return buildSection(TableIDPAT, 1, version, currentNext, 0, 0, body)
}

func pmtSection(program uint16, version uint8, currentNext bool, pcr uint16, streams ...ElementaryStream) []byte {
	body := []byte{0xe0 | byte(pcr>>8), byte(pcr), 0xf0, 0x00}
	for _, es := range streams {
		body = append(body, es.StreamType, 0xe0|byte(es.PID>>8), byte(es.PID), 0xf0, 0x00)
		body = append(body, es.Descriptors...)
	}
	return buildSection(TableIDPMT, program, version, currentNext, 0, 0, body)
}

func parseOne(t *testing.T, raw []byte) *Section {
	t.Helper()
	s, err := ParseSection(raw)
	if err != nil {
		t.Fatalf("ParseSection: %v", err)
	}
	return s
}

func TestParsePAT(t *testing.T) {
	raw := patSection(3, true, ProgramEntry{1, 0x100}, ProgramEntry{2, 0x200})
	pat, err := ParsePAT(parseOne(t, raw))
	if err != nil {
		t.Fatal(err)
	}
	if pat.Version != 3 || !pat.CurrentNext || pat.SectionNumber != 0 || pat.LastSectionNumber != 0 {
		t.Fatalf("unexpected header: %+v", pat)
	}
	if len(pat.Programs) != 2 || pat.Programs[1].ProgramNumber != 2 || pat.Programs[1].PID != 0x200 {
		t.Fatalf("unexpected programs: %+v", pat.Programs)
	}
}

func TestParsePATRejectsTrailingBytes(t *testing.T) {
	raw := buildSection(TableIDPAT, 1, 0, true, 0, 0, []byte{0, 1, 0xe1, 0x00, 0xaa})
	_, err := ParsePAT(parseOne(t, raw))
	if !errors.Is(err, ErrSectionLength) {
		t.Fatalf("want ErrSectionLength, got %v", err)
	}
}

func TestParsePATRejectsConflictingDuplicateProgram(t *testing.T) {
	body := []byte{0, 1, 0xe1, 0x00, 0, 1, 0xe2, 0x00}
	raw := buildSection(TableIDPAT, 1, 0, true, 0, 0, body)
	_, err := ParsePAT(parseOne(t, raw))
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("both PID")) {
		t.Fatalf("want conflict error, got %v", err)
	}
}

func TestParsePMT(t *testing.T) {
	es := []ElementaryStream{{StreamType: 0x1b, PID: 0x101}, {StreamType: 0x0f, PID: 0x102}}
	raw := pmtSection(101, 5, true, 0x100, es...)
	pmt, err := ParsePMT(parseOne(t, raw))
	if err != nil {
		t.Fatal(err)
	}
	if pmt.ProgramNumber != 101 || pmt.PCRPID != 0x100 || pmt.Version != 5 {
		t.Fatalf("unexpected PMT: %+v", pmt)
	}
	if len(pmt.Streams) != 2 || pmt.Streams[0].StreamType != 0x1b || pmt.Streams[1].PID != 0x102 {
		t.Fatalf("unexpected streams: %+v", pmt.Streams)
	}
}

func TestParsePMTRejectsBadInfoLength(t *testing.T) {
	body := []byte{0xe1, 0x00, 0xf0, 0x20, 0x1b, 0xe1, 0x01, 0xf0, 0x00}
	raw := buildSection(TableIDPMT, 101, 0, true, 0, 0, body)
	_, err := ParsePMT(parseOne(t, raw))
	if !errors.Is(err, ErrSectionLength) {
		t.Fatalf("want ErrSectionLength, got %v", err)
	}
}

func TestParsePMTRejectsDuplicateESPID(t *testing.T) {
	es := []ElementaryStream{{StreamType: 0x1b, PID: 0x101}, {StreamType: 0x0f, PID: 0x101}}
	raw := pmtSection(101, 0, true, 0x100, es...)
	_, err := ParsePMT(parseOne(t, raw))
	if err == nil {
		t.Fatal("want duplicate PID error")
	}
}

func TestParseSectionRejectsSectionNumberPastLast(t *testing.T) {
	raw := buildSection(TableIDPAT, 1, 0, true, 2, 1, []byte{0, 1, 0xe1, 0x00})
	if _, err := ParseSection(raw); !errors.Is(err, ErrSectionLength) {
		t.Fatalf("want ErrSectionLength, got %v", err)
	}
}
