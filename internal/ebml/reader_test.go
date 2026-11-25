package ebml_test

import (
	"testing"

	"mediacheck/internal/ebml"
	"mediacheck/internal/report"
	"mediacheck/internal/sample"
)

func TestVINTKnownAndUnknownLengths(t *testing.T) {
	r := ebml.NewReader([]byte{0x9F, 0x1F, 0xFF, 0xFF, 0xFF}, ebml.DefaultLimits())

	v, err := r.VINTAt(0, 8)
	if err != nil {
		t.Fatalf("VINTAt known: %v", err)
	}
	if v.Width != 1 || v.Value != 0x1F || v.Unknown {
		t.Fatalf("unexpected one-byte VINT: %+v", v)
	}

	u, err := r.VINTAt(1, 8)
	if err != nil {
		t.Fatalf("VINTAt unknown: %v", err)
	}
	if u.Width != 4 || !u.Unknown {
		t.Fatalf("expected four-byte unknown VINT, got %+v", u)
	}
}

func TestVINTRejectsZeroFirstByte(t *testing.T) {
	r := ebml.NewReader([]byte{0x00, 0x01}, ebml.DefaultLimits())
	_, err := r.VINTAt(0, 8)
	if err == nil {
		t.Fatal("expected invalid VINT error")
	}
	se, ok := report.AsError(err)
	if !ok || se.Record.ErrorCode != report.EBMLInvalidVINT {
		t.Fatalf("expected EBML_INVALID_VINT, got %#v", err)
	}
}

func TestParsePreservesUnknownBoundariesAndInterleavedOrder(t *testing.T) {
	bundle, err := sample.Generate(sample.CaseNormal, 7)
	if err != nil {
		t.Fatal(err)
	}
	doc, serr := ebml.ParseDocument(bundle.EBML)
	if serr != nil {
		t.Fatalf("ParseDocument: %v", serr)
	}
	if doc.Segment == nil || doc.Segment.Known {
		t.Fatalf("Segment must be unknown-length: %+v", doc.Segment)
	}
	if len(doc.Clusters) != 1 || doc.Clusters[0].Element.Known {
		t.Fatalf("Cluster must be one unknown-length element: %+v", doc.Clusters)
	}
	if got := doc.Clusters[0].Element.End; got != len(bundle.EBML) {
		t.Fatalf("unknown Cluster end=%d, want input end %d", got, len(bundle.EBML))
	}

	wantTracks := []uint64{1, 2, 2, 1, 2, 1}
	if len(doc.Blocks) != len(wantTracks) {
		t.Fatalf("blocks=%d, want %d", len(doc.Blocks), len(wantTracks))
	}
	for i, want := range wantTracks {
		if doc.Blocks[i].Track != want {
			t.Fatalf("block %d track=%d, want %d; raw interleave was reordered", i, doc.Blocks[i].Track, want)
		}
		if doc.Blocks[i].Index != i {
			t.Fatalf("block index %d != raw position %d", doc.Blocks[i].Index, i)
		}
	}
	if doc.Blocks[0].Element.Offset != 188 {
		t.Fatalf("first block offset=%d, want stable 188", doc.Blocks[0].Element.Offset)
	}
}

func TestTruncatedClusterHasStableStructuredLocation(t *testing.T) {
	bundle, err := sample.Generate(sample.CaseTruncatedCluster, 7)
	if err != nil {
		t.Fatal(err)
	}
	_, perr := ebml.ParseDocument(bundle.EBML)
	if perr == nil {
		t.Fatal("expected truncated cluster parse error")
	}
	se, ok := report.AsError(perr)
	if !ok {
		t.Fatalf("error is not structured report.Error: %T", perr)
	}
	rec := se.Record
	if rec.ErrorCode != report.EBMLTruncated {
		t.Fatalf("error code=%q want %q", rec.ErrorCode, report.EBMLTruncated)
	}
	if rec.ElementID != ebml.HexID(ebml.IDCluster) {
		t.Fatalf("element id=%q want Cluster %s", rec.ElementID, ebml.HexID(ebml.IDCluster))
	}
	if rec.InputOffset != 177 {
		t.Fatalf("input_offset=%d want stable Cluster offset 177", rec.InputOffset)
	}
	if rec.FailureOffset != len(bundle.EBML) || rec.FailureOffset != 366 {
		t.Fatalf("failure_offset=%d want stable truncation offset 366/len=%d", rec.FailureOffset, len(bundle.EBML))
	}
	if rec.ByteDelta != 64 {
		t.Fatalf("byte_delta=%d want 64 missing bytes", rec.ByteDelta)
	}
	if rec.Category != "truncated_element" {
		t.Fatalf("category=%q", rec.Category)
	}
}

func TestUnknownLengthOnSimpleBlockIsIllegalLength(t *testing.T) {
	data := append([]byte{}, unknownHeader(ebml.IDSegment, 8)...)
	data = append(data, unknownHeader(ebml.IDCluster, 4)...)
	blockOffset := len(data)
	data = append(data, 0xA3, 0xFF) // SimpleBlock with reserved unknown size

	_, err := ebml.ParseDocument(data)
	if err == nil {
		t.Fatal("expected illegal unknown-length error")
	}
	se, ok := report.AsError(err)
	if !ok {
		t.Fatalf("structured error: %T", err)
	}
	if se.Record.ErrorCode != report.EBMLIllegalLength || se.Record.ElementID != "0x000000A3" {
		t.Fatalf("unexpected error: %+v", se.Record)
	}
	if se.Record.InputOffset != blockOffset {
		t.Fatalf("input offset=%d want %d", se.Record.InputOffset, blockOffset)
	}
}

func TestIncompleteBlockIncludesTrackAndOffsets(t *testing.T) {
	data := append([]byte{}, unknownHeader(ebml.IDSegment, 8)...)
	data = append(data, unknownHeader(ebml.IDCluster, 4)...)
	blockOffset := len(data)
	data = append(data, 0xA3, 0x82, 0x81, 0x00) // size 2: track VINT + one byte only

	_, err := ebml.ParseDocument(data)
	if err == nil {
		t.Fatal("expected incomplete block error")
	}
	se, ok := report.AsError(err)
	if !ok {
		t.Fatalf("structured error: %T", err)
	}
	if se.Record.ErrorCode != report.EBMLIncompleteBlock {
		t.Fatalf("code=%q want %q", se.Record.ErrorCode, report.EBMLIncompleteBlock)
	}
	if se.Record.Track != 1 {
		t.Fatalf("track=%d want 1", se.Record.Track)
	}
	if se.Record.InputOffset != blockOffset {
		t.Fatalf("input_offset=%d want %d", se.Record.InputOffset, blockOffset)
	}
	if se.Record.FailureOffset != len(data) {
		t.Fatalf("failure_offset=%d want element end/input end %d", se.Record.FailureOffset, len(data))
	}
}

func TestUnknownClusterEndsAtNextLegalSegmentBoundary(t *testing.T) {
	data := append([]byte{}, unknownHeader(ebml.IDSegment, 8)...)
	data = append(data, unknownHeader(ebml.IDCluster, 4)...)
	data = append(data, 0xE7, 0x81, 0x00) // Timecode=0
	secondClusterOffset := len(data)
	data = append(data, unknownHeader(ebml.IDCluster, 4)...)

	doc, err := ebml.ParseDocument(data)
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}
	if len(doc.Clusters) != 2 {
		t.Fatalf("clusters=%d want 2", len(doc.Clusters))
	}
	if got := doc.Clusters[0].Element.End; got != secondClusterOffset {
		t.Fatalf("first unknown Cluster end=%d want next Cluster offset %d", got, secondClusterOffset)
	}
	if got := doc.Clusters[1].Element.Offset; got != secondClusterOffset {
		t.Fatalf("second Cluster offset=%d want %d", got, secondClusterOffset)
	}
}

func unknownHeader(id uint32, width int) []byte {
	var idBytes []byte
	switch {
	case id <= 0xFF:
		idBytes = []byte{byte(id)}
	case id <= 0xFFFF:
		idBytes = []byte{byte(id >> 8), byte(id)}
	default:
		idBytes = []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
	}
	raw := (uint64(1) << (7*width + 1)) - 1
	size := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		size[i] = byte(raw & 0xFF)
		raw >>= 8
	}
	return append(idBytes, size...)
}
