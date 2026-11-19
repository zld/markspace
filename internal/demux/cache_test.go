package demux

import (
	"errors"
	"io"
	"testing"

	"tsinspect/internal/transport"
)

func TestMidStreamStartUsesCache(t *testing.T) {
	// ES packets precede the first PAT/PMT; the slicer must cache and flush.
	var b streamBuilder
	esCC, patCC, pmtCC := uint8(0), uint8(0), uint8(0)
	b.add(esPackets(0x100, &esCC, 5))
	b.add(psiPackets(0, &patCC, patBytes(0, [2]uint16{1, 0x1000})))
	b.add(psiPackets(0x1000, &pmtCC, pmtBytes(1, 0, 0x100, []esDef{{0x100, 0x1b}})))
	b.add(esPackets(0x100, &esCC, 5))

	res, err := runPipeline(t, b.bytes(), 1, io.Discard, 32)
	if err != nil {
		t.Fatal(err)
	}
	if res.Slice.BufferedFlushed != 5 {
		t.Fatalf("buffered flushed = %d, want 5", res.Slice.BufferedFlushed)
	}
	if res.Slice.ESPackets != 10 {
		t.Fatalf("es packets = %d, want 10", res.Slice.ESPackets)
	}
	if res.Slice.BufferOverflows != 0 {
		t.Fatalf("unexpected overflows: %+v", res.Slice)
	}
}

func TestBufferCapDropsHeadWhenNoTableArrives(t *testing.T) {
	var b streamBuilder
	esCC := uint8(0)
	b.add(esPackets(0x100, &esCC, 20))
	res, err := runPipeline(t, b.bytes(), 1, io.Discard, 8)
	if err != nil {
		t.Fatal(err)
	}
	if res.Slice.BufferOverflows == 0 || res.Slice.BufferedDropped < 8 {
		t.Fatalf("buffer cap not enforced: %+v", res.Slice)
	}
	if res.Slice.PacketsWritten != 0 {
		t.Fatalf("packets written without a table: %d", res.Slice.PacketsWritten)
	}
}

func TestPartialTailIsReported(t *testing.T) {
	var b streamBuilder
	esCC := uint8(0)
	b.add(esPackets(0x100, &esCC, 2))
	b.raw([]byte{0x47, 0x00, 0x11})
	_, err := runPipeline(t, b.bytes(), 0, nil, 8)
	var pt *transport.PartialTailError
	if !errors.As(err, &pt) || pt.Bytes != 3 {
		t.Fatalf("want partial tail of 3 bytes, got %v", err)
	}
}
