package ebml

import (
	"testing"

	"tracecheck/internal/serr"
)

// buildMiniDoc builds: EBML header + Segment(unknown) containing one Cluster.
func buildMiniDoc(t *testing.T) ([]byte, *Document, *Element, *Element) {
	t.Helper()
	var seg Builder
	if err := seg.Container(IDCluster, func(c *Builder) error {
		if err := c.UintElement(IDTimestamp, 0); err != nil {
			return err
		}
		return c.Element(IDSimpleBlock, []byte{0x81, 0x00, 0x00, 0xAA})
	}); err != nil {
		t.Fatal(err)
	}
	var top Builder
	if err := top.Container(IDEBMLHeader, func(h *Builder) error {
		return h.StringElement(IDDocType, "webm")
	}); err != nil {
		t.Fatal(err)
	}
	if err := top.UnknownElement(IDSegment, seg.Bytes()); err != nil {
		t.Fatal(err)
	}
	data := top.Bytes()
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	segment := doc.Find(IDSegment)
	if segment == nil || !segment.UnknownSize {
		t.Fatalf("segment unknown size not detected: %+v", segment)
	}
	// Unknown-length segment must extend exactly to EOF.
	if segment.PayloadEnd != int64(len(data)) {
		t.Fatalf("unknown segment end=%d, want EOF %d", segment.PayloadEnd, len(data))
	}
	cluster := doc.Find(IDSegment, IDCluster)
	if cluster == nil {
		t.Fatal("cluster not found inside unknown segment")
	}
	return data, doc, segment, cluster
}

func TestParseUnknownLengthSegment(t *testing.T) {
	data, doc, segment, cluster := buildMiniDoc(t)
	if int64(len(data)) != segment.PayloadEnd {
		t.Fatalf("document length %d != segment end %d", len(data), segment.PayloadEnd)
	}
	if cluster.PayloadEnd > segment.PayloadEnd {
		t.Fatalf("cluster end %d past segment end %d", cluster.PayloadEnd, segment.PayloadEnd)
	}
	if segment.PayloadOffset != cluster.Offset {
		t.Errorf("segment payload starts at %d, cluster at %d", segment.PayloadOffset, cluster.Offset)
	}
	if got := PathString(doc.PathOf(cluster)); got != "Segment/Cluster" {
		t.Errorf("cluster path = %q", got)
	}
}

func TestParseOffsets(t *testing.T) {
	data, doc, _, cluster := buildMiniDoc(t)
	block := doc.Find(IDSegment, IDCluster, IDSimpleBlock)
	if block == nil {
		t.Fatal("block missing")
	}
	if block.Offset >= int64(len(data)) {
		t.Fatalf("block offset %d outside data length %d", block.Offset, len(data))
	}
	if block.Offset < cluster.Offset {
		t.Fatal("block offset precedes its cluster")
	}
	// Block payload begins right after its 3-byte-ish header and holds 0xAA.
	payload := doc.Payload(block)
	if len(payload) != 4 || payload[3] != 0xAA {
		t.Errorf("block payload = % x", payload)
	}
	if block.Offset+int64(block.HeaderSize) != block.PayloadOffset {
		t.Error("header geometry inconsistent")
	}
}

func TestParseTruncatedHeader(t *testing.T) {
	// Valid ID VINT start (4-byte marker 0x1A) but no remaining bytes at all.
	data := []byte{0x1A}
	_, err := Parse(data)
	e, ok := serr.As(err)
	if !ok {
		t.Fatalf("expected structured error, got %v", err)
	}
	if e.Code != serr.CodeTruncatedHeader || e.Offset != 0 {
		t.Fatalf("code=%s offset=%d", e.Code, e.Offset)
	}
}

func TestParseIllegalVINT(t *testing.T) {
	// 0x00 implies a 9-byte VINT width, which is illegal.
	data := []byte{0x00, 0x01}
	_, err := Parse(data)
	e, ok := serr.As(err)
	if !ok || e.Code != serr.CodeIllegalVINT || e.Offset != 0 {
		t.Fatalf("got %v", err)
	}
}

func TestParseLengthOutOfBounds(t *testing.T) {
	// SimpleBlock declares a 10-byte payload but only 2 follow.
	var b Builder
	if err := b.Element(IDSimpleBlock, nil); err != nil {
		t.Fatal(err)
	}
	// Manually craft: ID + size=10 + 2 payload bytes.
	idBytes, _ := EncodeID(IDSimpleBlock)
	sizeBytes, _ := EncodeVINT(10)
	raw := append(append(append([]byte{}, idBytes...), sizeBytes...), 0xAA, 0xBB)
	_, err := Parse(raw)
	e, ok := serr.As(err)
	if !ok || e.Code != serr.CodeLengthOutOfBounds || e.Offset != 0 {
		t.Fatalf("got %v", err)
	}
}

func TestParsePathInError(t *testing.T) {
	// Segment with a Cluster whose block declares an oversized payload.
	idBytes, _ := EncodeID(IDSimpleBlock)
	sizeBytes, _ := EncodeVINT(100)
	badBlock := append(append(append([]byte{}, idBytes...), sizeBytes...), 0x01)
	var cluster Builder
	if err := cluster.Element(IDCluster, badBlock); err != nil {
		t.Fatal(err)
	}
	var seg Builder
	if err := seg.UnknownElement(IDSegment, cluster.Bytes()); err != nil {
		t.Fatal(err)
	}
	_, err := Parse(seg.Bytes())
	e, ok := serr.As(err)
	if !ok {
		t.Fatalf("got %v", err)
	}
	if e.Code != serr.CodeLengthOutOfBounds {
		t.Fatalf("code = %s", e.Code)
	}
	if len(e.Path) < 2 || e.Path[0] != "Segment" || e.Path[1] != "Cluster" {
		t.Fatalf("path = %v", e.Path)
	}
	if e.Offset < 0 {
		t.Fatalf("offset missing: %d", e.Offset)
	}
}

// blockBody builds a SimpleBlock payload: track 1, timecode 0, flags 0x80.
func blockBody(payload ...byte) []byte {
	return append([]byte{0x81, 0x00, 0x00, 0x80}, payload...)
}

// segmentWithClusters builds EBML header + unknown-length Segment holding the
// supplied clusters, each written as an unknown-length Cluster.
func segmentWithClusters(t *testing.T, clusters ...[]byte) []byte {
	t.Helper()
	var seg Builder
	for _, c := range clusters {
		if err := seg.UnknownElement(IDCluster, c); err != nil {
			t.Fatal(err)
		}
	}
	var top Builder
	if err := top.UnknownElement(IDSegment, seg.Bytes()); err != nil {
		t.Fatal(err)
	}
	return top.Bytes()
}

func TestParseUnknownLengthSiblings(t *testing.T) {
	var c1, c2 Builder
	if err := c1.Element(IDSimpleBlock, blockBody(0xAA)); err != nil {
		t.Fatal(err)
	}
	if err := c2.Element(IDSimpleBlock, blockBody(0xBB)); err != nil {
		t.Fatal(err)
	}
	data := segmentWithClusters(t, c1.Bytes(), c2.Bytes())
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	clusters := doc.All(IDCluster)
	if len(clusters) != 2 {
		t.Fatalf("got %d clusters, want 2", len(clusters))
	}
	// The first unknown-length Cluster must stop at the second Cluster's ID.
	if clusters[0].PayloadEnd != clusters[1].Offset {
		t.Errorf("cluster 0 end=%d, want next sibling at %d",
			clusters[0].PayloadEnd, clusters[1].Offset)
	}
	if !clusters[0].UnknownSize || !clusters[1].UnknownSize {
		t.Error("both clusters should be unknown length")
	}
	// Blocks must land inside their own cluster.
	b0 := doc.Find(IDSegment, IDCluster, IDSimpleBlock)
	if b0 == nil || doc.Payload(b0)[4] != 0xAA {
		t.Fatalf("first block not in first cluster: %+v", b0)
	}
	if b0.Offset >= clusters[1].Offset {
		t.Error("first block offset escaped its cluster boundary")
	}
	if clusters[1].PayloadEnd != int64(len(data)) {
		t.Errorf("last cluster end=%d, want EOF %d", clusters[1].PayloadEnd, len(data))
	}
}

func TestParseParentBoundaryConflict(t *testing.T) {
	// An unknown-length Cluster followed by TrackEntry: TrackEntry is neither
	// a legal child of Cluster nor of Segment, so it cannot close the Cluster
	// and must be reported as a boundary conflict.
	var block Builder
	if err := block.Element(IDSimpleBlock, blockBody(0xAA)); err != nil {
		t.Fatal(err)
	}
	var te Builder
	if err := te.UintElement(IDTrackNumber, 1); err != nil {
		t.Fatal(err)
	}
	var track Builder
	if err := track.Element(IDTrackEntry, te.Bytes()); err != nil {
		t.Fatal(err)
	}
	body := append(func() []byte {
		var c Builder
		_ = c.UnknownElement(IDCluster, block.Bytes())
		return c.Bytes()
	}(), track.Bytes()...)
	var seg Builder
	_ = seg.UnknownElement(IDSegment, body)
	_, err := Parse(seg.Bytes())
	e, ok := serr.As(err)
	if !ok {
		t.Fatalf("expected structured error, got %v", err)
	}
	if e.Code != serr.CodeParentBoundary {
		t.Fatalf("code = %s (%v)", e.Code, err)
	}
	if e.Offset < 0 {
		t.Fatal("boundary conflict must carry a byte offset")
	}
	if len(e.Path) == 0 || e.Path[len(e.Path)-1] != "TrackEntry" {
		t.Fatalf("path = %v", e.Path)
	}
}

func TestParseUnknownScalarRunsToBoundary(t *testing.T) {
	// An unknown-length non-container element has no nested boundary; it is
	// bounded by its parent and must not swallow following siblings when the
	// parent is known length.
	var inner Builder
	if err := inner.UnknownElement(IDDocType, []byte("matroska")); err != nil {
		t.Fatal(err)
	}
	var header Builder
	_ = header.Element(IDEBMLHeader, inner.Bytes())
	data := header.Bytes()
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	el := doc.Find(IDEBMLHeader, IDDocType)
	if el == nil || !el.UnknownSize {
		t.Fatalf("unknown scalar not found: %+v", el)
	}
	if el.PayloadEnd != int64(len(data)) {
		t.Errorf("unknown scalar end=%d, want parent end %d", el.PayloadEnd, len(data))
	}
}
