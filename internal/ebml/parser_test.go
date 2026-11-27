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
