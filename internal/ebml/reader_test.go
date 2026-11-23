package ebml_test

import (
	"testing"

	"mediad/internal/ebml"
	"mediad/internal/merr"
	"mediad/internal/testkit"
)

func TestSimpleTree(t *testing.T) {
	seg := testkit.Master(ebml.IDSegment,
		testkit.Master(ebml.IDInfo, testkit.Uint(ebml.IDTimecodeScale, 1000000)),
		testkit.Master(ebml.IDCluster,
			testkit.Uint(ebml.IDTimecode, 1234),
			testkit.Bytes(ebml.IDSimpleBlock, []byte{0x81, 0x00, 0x00, 0x80, 1, 2, 3}),
		),
	)
	root := testkit.Master(0x1A45DFA3, testkit.Str(ebml.IDDocType, "webm"))
	root.Children = append(root.Children, seg)
	data, err := testkit.Serialize(root)
	if err != nil {
		t.Fatal(err)
	}
	evs, err := collect(t, data)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	// EBML start/docType/end, Segment start, Info start/scale/end, Cluster
	// start/timecode/block/end, Segment end, doc end => 12 events.
	if got, want := len(evs), 12; got != want {
		t.Fatalf("event count = %d, want %d: %v", got, want, evs)
	}
	var block ebml.Event
	for _, ev := range evs {
		if ev.Kind == ebml.Leaf && ev.ID == ebml.IDSimpleBlock {
			block = ev
		}
	}
	if block.Size != 7 || len(block.Data) != 7 {
		t.Fatalf("block payload = %d bytes", len(block.Data))
	}
	if block.Depth != 3 {
		t.Fatalf("block depth = %d, want 3 (EBML > Segment > Cluster)", block.Depth)
	}
	if block.DataOffset != block.HeaderOffset+2 {
		t.Fatalf("data offset %d not right after a 2-byte block header (header at %d)",
			block.DataOffset, block.HeaderOffset)
	}
}

func TestUnknownSizeSegmentAndCluster(t *testing.T) {
	seg := testkit.Master(ebml.IDSegment,
		testkit.Master(ebml.IDInfo, testkit.Uint(ebml.IDTimecodeScale, 1000000)),
	)
	seg.UnknownSize = true
	cl := testkit.Master(ebml.IDCluster,
		testkit.Uint(ebml.IDTimecode, 100),
		testkit.Bytes(ebml.IDSimpleBlock, []byte{0x81, 0, 0, 0x80, 9}),
	)
	cl.UnknownSize = true
	cl2 := testkit.Master(ebml.IDCluster,
		testkit.Uint(ebml.IDTimecode, 140),
		testkit.Bytes(ebml.IDSimpleBlock, []byte{0x81, 0, 0, 0x80, 9}),
	)
	cl2.UnknownSize = true
	seg.Children = append(seg.Children, cl, cl2)
	root := testkit.Master(0x1A45DFA3, testkit.Str(ebml.IDDocType, "matroska"))
	root.Children = append(root.Children, seg)
	data, err := testkit.Serialize(root)
	if err != nil {
		t.Fatal(err)
	}
	evs, err := collect(t, data)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	var starts, ends int
	var unknownStarts int
	for _, ev := range evs {
		switch {
		case ev.Kind == ebml.Start && ev.ID == ebml.IDCluster:
			starts++
			if ev.Unknown {
				unknownStarts++
			}
		case ev.Kind == ebml.End && ev.ID == ebml.IDCluster:
			ends++
		}
	}
	if starts != 2 || ends != 2 || unknownStarts != 2 {
		t.Fatalf("cluster starts=%d ends=%d unknown=%d", starts, ends, unknownStarts)
	}
	// The unknown-size segment must end cleanly before the EBML header's end.
	last := evs[len(evs)-2]
	if last.Kind != ebml.End || last.ID != ebml.IDSegment {
		t.Fatalf("expected segment end before document end, got %v", last)
	}
	if final := evs[len(evs)-1]; final.Kind != ebml.End || final.ID != ebml.IDEBML {
		t.Fatalf("expected document end last, got %v", final)
	}
}

func TestTruncatedClusterReportsOffset(t *testing.T) {
	seg := testkit.Master(ebml.IDSegment)
	cl := testkit.Master(ebml.IDCluster,
		testkit.Uint(ebml.IDTimecode, 10),
		testkit.Bytes(ebml.IDSimpleBlock, make([]byte, 64)),
	)
	cl.UnknownSize = true
	seg.Children = []*testkit.Element{cl}
	seg.UnknownSize = true
	root := testkit.Master(0x1A45DFA3)
	root.Children = []*testkit.Element{seg}
	data, err := testkit.Serialize(root)
	if err != nil {
		t.Fatal(err)
	}
	cut := data[:len(data)-10] // chop the SimpleBlock payload
	evs, err := collect(t, cut)
	if err == nil {
		t.Fatal("expected truncation error")
	}
	if code := merr.CodeOf(err); code != merr.CodeTruncatedCluster {
		t.Fatalf("error code = %s, want %s (%v)", code, merr.CodeTruncatedCluster, err)
	}
	if off := merr.OffsetOf(err); off <= 0 || off > int64(len(cut)) {
		t.Fatalf("offset %d not within truncated input", off)
	}
	// Confirmed structure before the cut must still be delivered.
	if len(evs) < 3 {
		t.Fatalf("expected confirmed events before truncation, got %d", len(evs))
	}
}
