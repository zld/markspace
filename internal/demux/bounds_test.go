package demux

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"tsinspect/internal/transport"
)

func TestSectionLengthIsBounded(t *testing.T) {
	// A malformed section declaring the maximum length but never completing
	// must not grow the assembler buffer or be accepted.
	var b streamBuilder
	patCC := uint8(0)
	b.add(psiPackets(0, &patCC, patBytes(0, [2]uint16{1, 0x1000})))
	// pointer_field 0, table_id 0x02, section_length 0x0fff, then padding.
	evil := make([]byte, 184)
	evil[0] = 0
	evil[1] = 0x02
	evil[2] = 0xbf
	evil[3] = 0xff
	b.add([][]byte{tsPacket(0x1000, 0, true, evil)})

	res, err := runPipeline(t, b.bytes(), 0, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	if res.Programs.PMT(1) != nil {
		t.Fatal("oversized section was accepted")
	}
	if res.SectionErrors == 0 {
		t.Fatalf("oversized section not reported: %+v", res)
	}
}

func TestAssemblerTableIsBounded(t *testing.T) {
	if MaxPSIPIDs <= 0 {
		t.Fatal("PSI PID table must have a positive bound")
	}
}

func TestSliceWriterFailureIsReported(t *testing.T) {
	var b streamBuilder
	patCC, pmtCC, esCC := uint8(0), uint8(0), uint8(0)
	b.add(psiPackets(0, &patCC, patBytes(0, [2]uint16{1, 0x1000})))
	b.add(psiPackets(0x1000, &pmtCC, pmtBytes(1, 0, 0x100, []esDef{{0x100, 0x1b}})))
	b.add(esPackets(0x100, &esCC, 4))

	w := &failingWriter{after: 1}
	p := NewPipeline(bytes.NewReader(b.bytes()), Config{Program: 1, MaxBufferPackets: 8, SliceToDisk: true}, w)
	res, err := p.Run()
	if err == nil || !errors.Is(err, errWriteFailed) {
		t.Fatalf("want write failure, got %v", err)
	}
	if res == nil || res.Slice.PacketsWritten == 0 && res.Slice.PMTPackets == 0 {
		t.Fatalf("result not populated on write failure")
	}
}

var errWriteFailed = errors.New("write failed")

type failingWriter struct {
	writes int
	after  int
}

func (f *failingWriter) Write(p []byte) (int, error) {
	f.writes++
	if f.writes > f.after {
		return len(p), errWriteFailed
	}
	return len(p), nil
}

func TestOrphanPMTBeforePATEventuallyAdopted(t *testing.T) {
	var b streamBuilder
	pmtCC, patCC, esCC := uint8(0), uint8(0), uint8(0)
	// PMT arrives before any PAT; it is an orphan until the PAT is applied.
	b.add(psiPackets(0x1000, &pmtCC, pmtBytes(1, 0, 0x100, []esDef{{0x100, 0x1b}})))
	b.add(psiPackets(0, &patCC, patBytes(0, [2]uint16{1, 0x1000})))
	b.add(esPackets(0x100, &esCC, 3))

	res, err := runPipeline(t, b.bytes(), 1, io.Discard, 16)
	if err != nil {
		t.Fatal(err)
	}
	if res.Programs.OrphanPMTs() != 1 {
		t.Fatalf("orphan count = %d, want 1", res.Programs.OrphanPMTs())
	}
	if pmt := res.Programs.PMT(1); pmt == nil || len(pmt.Streams) != 1 {
		t.Fatalf("orphan PMT never adopted: %+v", pmt)
	}
}

func TestPSIOnPESIsIgnored(t *testing.T) {
	pes := &transport.Packet{PUSI: true, Payload: []byte{0x00, 0x00, 0x01, 0xe0}}
	if looksLikePMTSection(pes) {
		t.Fatal("PES start code misdetected as PMT")
	}
	pmtStart := &transport.Packet{PUSI: true, Payload: []byte{0x00, 0x02, 0xb0, 0x11}}
	if !looksLikePMTSection(pmtStart) {
		t.Fatal("real PMT start not recognised")
	}
	// Without PUSI there is no pointer_field, so no section can start.
	cont := &transport.Packet{PUSI: false, Payload: []byte{0x00, 0x02, 0xb0, 0x11}}
	if looksLikePMTSection(cont) {
		t.Fatal("continuation packet treated as a section start")
	}
}
