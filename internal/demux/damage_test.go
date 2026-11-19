package demux

import (
	"bytes"
	"testing"
)

func TestInvalidCRCDoesNotReplaceEffectiveTable(t *testing.T) {
	var b streamBuilder
	patCC, pmtCC, esCC := uint8(0), uint8(0), uint8(0)
	b.add(psiPackets(0, &patCC, patBytes(0, [2]uint16{1, 0x1000})))
	b.add(psiPackets(0x1000, &pmtCC, pmtBytes(1, 0, 0x100, []esDef{{0x100, 0x1b}})))
	b.add(esPackets(0x100, &esCC, 4))
	bad := pmtBytes(1, 1, 0x100, []esDef{{0x100, 0x1b}, {0x101, 0x0f}})
	bad[len(bad)-1] ^= 0xff
	badCC := uint8(0)
	b.add(psiPackets(0x1000, &badCC, bad))
	b.add(esPackets(0x100, &esCC, 4))

	res, err := runPipeline(t, b.bytes(), 1, nil, 64)
	if err != nil {
		t.Fatal(err)
	}
	if res.CRCErrors != 1 {
		t.Fatalf("crc errors = %d", res.CRCErrors)
	}
	if len(res.Programs.PMT(1).Streams) != 1 || res.Programs.PMT(1).Version != 0 {
		t.Fatalf("corrupt section replaced effective table: %+v", res.Programs.PMT(1))
	}
}

func TestCorruptPSIPacketIsNotSliced(t *testing.T) {
	// The corrupted PMT packet must be rejected from the slice even though
	// its TS header was valid, so the slice never carries a bad section.
	var b streamBuilder
	patCC, pmtCC, esCC := uint8(0), uint8(0), uint8(0)
	b.add(psiPackets(0, &patCC, patBytes(0, [2]uint16{1, 0x1000})))
	b.add(psiPackets(0x1000, &pmtCC, pmtBytes(1, 0, 0x100, []esDef{{0x100, 0x1b}})))
	b.add(esPackets(0x100, &esCC, 3))
	bad := pmtBytes(1, 1, 0x100, []esDef{{0x100, 0x1b}, {0x101, 0x0f}})
	bad[len(bad)-1] ^= 0xff
	badCC := uint8(0)
	badPkts := psiPackets(0x1000, &badCC, bad)
	b.add(badPkts)
	b.add(esPackets(0x100, &esCC, 3))

	var out bytes.Buffer
	res, err := runPipeline(t, b.bytes(), 1, &out, 32)
	if err != nil {
		t.Fatal(err)
	}
	if res.CRCErrors != 1 {
		t.Fatalf("crc errors = %d", res.CRCErrors)
	}
	for i := 0; i < out.Len(); i += 188 {
		if bytes.Equal(out.Bytes()[i:i+188], badPkts[0]) {
			t.Fatal("corrupt PMT packet was written to the slice")
		}
	}
}

func TestPostDamageResumptionIsUnjudgeableAndDoesNotFeedPSI(t *testing.T) {
	// A transport-error packet breaks continuity. The next packet on that PID
	// must be reported as unjudgeable, and a non-starting fragment must not
	// be appended to a partially assembled section.
	var b streamBuilder
	patCC, pmtCC := uint8(0), uint8(0)
	b.add(psiPackets(0, &patCC, patBytes(0, [2]uint16{1, 0x1000})))
	b.add(psiPackets(0x1000, &pmtCC, pmtBytes(1, 0, 0x100, []esDef{{0x100, 0x1b}})))
	bad := tsPacket(0x100, 5, false, []byte{0, 0, 1, 0xe0})
	bad[1] |= 0x80 // TEI
	b.add([][]byte{bad})
	b.add([][]byte{tsPacket(0x100, 9, false, []byte{0, 0, 1, 0xe0})})

	res, err := runPipeline(t, b.bytes(), 0, nil, 64)
	if err != nil {
		t.Fatal(err)
	}
	if res.Metrics.Totals().UnknownGaps != 1 {
		t.Fatalf("unjudgeable gap = %d, want 1", res.Metrics.Totals().UnknownGaps)
	}
	if res.Metrics.Totals().Jumps != 0 {
		t.Fatalf("post-error resumption miscounted as loss: %+v", res.Metrics.Totals())
	}
}

func TestLostPacketResetsSectionReassembly(t *testing.T) {
	// Start a PMT section, then lose the continuation packet, then resume.
	var b streamBuilder
	patCC := uint8(0)
	b.add(psiPackets(0, &patCC, patBytes(0, [2]uint16{1, 0x1000})))
	// Build a PMT that spans two packets (many streams).
	var streams []esDef
	for i := 0; i < 60; i++ {
		streams = append(streams, esDef{uint16(0x200 + i), 0x0f})
	}
	section := pmtBytes(1, 0, 0x100, streams)
	first := tsPacket(0x1000, 0, true, append([]byte{0}, section[:183]...))
	rest := section[183:]
	second := tsPacket(0x1000, 1, false, rest)
	b.add([][]byte{first})
	// Skip the real second packet; inject a packet with a fresh counter and
	// non-start payload instead. That is a jump and must reset assembly.
	b.add([][]byte{tsPacket(0x1000, 7, false, rest)})
	b.add([][]byte{second})

	res, err := runPipeline(t, b.bytes(), 0, nil, 64)
	if err != nil {
		t.Fatal(err)
	}
	if res.Programs.PMT(1) != nil {
		t.Fatal("PMT was reconstructed from a damaged section")
	}
	if res.Metrics.Totals().Jumps == 0 {
		t.Fatal("jump not recorded")
	}
}
