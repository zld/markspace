package demux

import (
	"bytes"
	"io"
	"testing"
)

func TestPipelineEndToEnd(t *testing.T) {
	var b streamBuilder
	patCC, pmtCC, esCC := uint8(0), uint8(0), uint8(0)
	b.add(psiPackets(0, &patCC, patBytes(0, [2]uint16{1, 0x1000}, [2]uint16{2, 0x1001})))
	b.add(psiPackets(0x1000, &pmtCC, pmtBytes(1, 0, 0x100, []esDef{{0x100, 0x1b}, {0x101, 0x0f}})))
	b.add(esPackets(0x100, &esCC, 10))
	b.add(esPackets(0x101, &esCC, 8))
	b.add(psiPackets(0, &patCC, patBytes(1, [2]uint16{1, 0x1000}, [2]uint16{2, 0x1001})))
	pmtCC2 := uint8(0)
	b.add(psiPackets(0x1000, &pmtCC2, pmtBytes(1, 1, 0x100, []esDef{{0x100, 0x1b}, {0x101, 0x0f}, {0x102, 0x0f}})))
	esCC2 := uint8(0)
	b.add(esPackets(0x102, &esCC2, 5))

	res, err := runPipeline(t, b.bytes(), 1, io.Discard, 64)
	if err != nil {
		t.Fatal(err)
	}
	if res.Packets != int64(len(b.bytes())/188) {
		t.Fatalf("packets = %d", res.Packets)
	}
	if res.Programs.PATVersionChanges() != 1 || res.Programs.PMTVersionChanges() != 1 {
		t.Fatalf("changes pat=%d pmt=%d", res.Programs.PATVersionChanges(), res.Programs.PMTVersionChanges())
	}
	pmt := res.Programs.PMT(1)
	if pmt == nil || len(pmt.Streams) != 3 || pmt.Version != 1 {
		t.Fatalf("effective PMT wrong: %+v", pmt)
	}
	if res.Slice.PMTPackets == 0 || res.Slice.ESPackets != 23 {
		t.Fatalf("slice stats: %+v", res.Slice)
	}
	if len(res.Slice.Boundaries) != 2 {
		t.Fatalf("boundaries = %d, want 2", len(res.Slice.Boundaries))
	}
}

func TestSliceExcludesOtherPrograms(t *testing.T) {
	var b streamBuilder
	patCC, p1CC, p2CC, esA, esB := uint8(0), uint8(0), uint8(0), uint8(0), uint8(0)
	b.add(psiPackets(0, &patCC, patBytes(0, [2]uint16{1, 0x1000}, [2]uint16{2, 0x1001})))
	b.add(psiPackets(0x1000, &p1CC, pmtBytes(1, 0, 0x100, []esDef{{0x100, 0x1b}})))
	b.add(psiPackets(0x1001, &p2CC, pmtBytes(2, 0, 0x200, []esDef{{0x200, 0x1b}})))
	b.add(esPackets(0x100, &esA, 6))
	b.add(esPackets(0x200, &esB, 6))
	b.add([][]byte{tsPacket(0x550, 0, false, []byte{1, 2, 3})}) // unrelated PID

	var out bytes.Buffer
	if _, err := runPipeline(t, b.bytes(), 1, &out, 64); err != nil {
		t.Fatal(err)
	}
	data := out.Bytes()
	if len(data)%188 != 0 || len(data) == 0 {
		t.Fatalf("slice size %d", len(data))
	}
	for i := 0; i < len(data); i += 188 {
		pid := uint16(data[i+1]&0x1f)<<8 | uint16(data[i+2])
		if pid != 0x100 && pid != 0x1000 {
			t.Fatalf("slice contains PID %#x from another program", pid)
		}
	}
}
