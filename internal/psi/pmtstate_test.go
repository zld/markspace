package psi

import "testing"

func pmtSection(program uint16, version uint8, number, last uint8, pcr uint16, streams ...[2]uint16) Section {
	return buildSection(TableIDPMT, program, version, true, number, last, pmtBody(pcr, streams...))
}

func TestTablesMultiSectionPMT(t *testing.T) {
	tb := NewTables()
	if _, err := tb.AddPAT(patSection(0, 0, 0, [2]uint16{1, 0x100})); err != nil {
		t.Fatal(err)
	}
	if _, err := tb.AddPMT(0x100, pmtSection(1, 0, 0, 1, 0x101, [2]uint16{0x1b, 0x101})); err != nil {
		t.Fatal(err)
	}
	if _, ok := tb.PMT(1); ok {
		t.Fatal("incomplete multi-section PMT activated")
	}
	if _, err := tb.AddPMT(0x100, pmtSection(1, 0, 1, 1, 0x101, [2]uint16{0x0f, 0x102})); err != nil {
		t.Fatal(err)
	}
	p, ok := tb.PMT(1)
	if !ok || len(p.Streams) != 2 {
		t.Fatalf("merged PMT = %+v, %v", p, ok)
	}
	if p.Streams[0].PID != 0x101 || p.Streams[1].PID != 0x102 {
		t.Fatalf("streams not merged/sorted: %+v", p.Streams)
	}
}

func TestTablesPMTRequiresMatchingPAT(t *testing.T) {
	tb := NewTables()
	if _, err := tb.AddPAT(patSection(0, 0, 0, [2]uint16{1, 0x100})); err != nil {
		t.Fatal(err)
	}
	// PMT arrives on the wrong PID for program 1.
	if _, err := tb.AddPMT(0x200, pmtSection(1, 2, 0, 0, 0x101, [2]uint16{0x1b, 0x101})); err != nil {
		t.Fatal(err)
	}
	if _, ok := tb.PMT(1); ok {
		t.Fatal("PMT accepted on PID not mapped by PAT")
	}
	// Correct PID activates it.
	if _, err := tb.AddPMT(0x100, pmtSection(1, 2, 0, 0, 0x101, [2]uint16{0x1b, 0x101}, [2]uint16{0x0f, 0x102})); err != nil {
		t.Fatal(err)
	}
	p, ok := tb.PMT(1)
	if !ok || p.PCRPID != 0x101 || len(p.Streams) != 2 {
		t.Fatalf("PMT = %+v, %v", p, ok)
	}
	if tb.Stats().PMTVersionChanges != 0 {
		t.Fatalf("initial PMT counted as version change")
	}
	if _, err := tb.AddPMT(0x100, pmtSection(1, 3, 0, 0, 0x101, [2]uint16{0x1b, 0x101})); err != nil {
		t.Fatal(err)
	}
	if tb.Stats().PMTVersionChanges != 1 {
		t.Fatalf("PMT version changes = %d", tb.Stats().PMTVersionChanges)
	}
}
