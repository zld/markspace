package ebml

import (
	"bytes"
	"testing"

	"mediapack/internal/fault"
)

// elementHeader encodes an EBML id and a known size (fixture sizes stay <=
// 127 so the size VINT occupies one byte). The ID width is derived from its
// own length marker, supporting 1..4 byte Matroska IDs.
func elementHeader(id uint32, size int) []byte {
	var raw []byte
	switch {
	case id <= 0xFF:
		raw = []byte{byte(id)}
	case id <= 0xFFFF:
		raw = []byte{byte(id >> 8), byte(id)}
	case id <= 0xFFFFFF:
		raw = []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	default:
		raw = []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
	}
	return append(raw, byte(0x80|size))
}

// buildBlock makes a non-laced block payload: track VINT, int16 rel tc, flags,
// codec data.
func buildBlock(track uint8, rel int16, keyframe bool, payload []byte) []byte {
	flags := byte(0)
	if keyframe {
		flags |= 0x80
	}
	out := []byte{0x80 | track, byte(rel >> 8), byte(rel), flags}
	return append(out, payload...)
}

// buildWebM assembles a minimal stream: EBML header + Segment containing one
// cluster with two blocks. unknownLen makes Segment and Cluster use 0xFF
// size markers, terminating the cluster at EOF.
func buildWebM(unknownLen bool, blocks ...[]byte) []byte {
	ebmlHdr := elementHeader(IDEBMLHeader, 0)
	var clusterBody []byte
	ts := elementHeader(IDTimestamp, 2)
	clusterBody = append(clusterBody, ts...)
	clusterBody = append(clusterBody, 0x00, 0x64) // timestamp = 100
	for _, blk := range blocks {
		clusterBody = append(clusterBody, elementHeader(IDSimpleBlock, len(blk))...)
		clusterBody = append(clusterBody, blk...)
	}
	var clusterHdr []byte
	if unknownLen {
		clusterHdr = []byte{0x1F, 0x43, 0xB6, 0x75, 0xFF}
	} else {
		clusterHdr = elementHeader(IDCluster, len(clusterBody))
	}
	segmentBody := append(clusterHdr, clusterBody...)
	var segHdr []byte
	if unknownLen {
		segHdr = []byte{0x18, 0x53, 0x80, 0x67, 0xFF}
	} else {
		segHdr = elementHeader(IDSegment, len(segmentBody))
	}
	return append(ebmlHdr, append(segHdr, segmentBody...)...)
}

func TestScanKnownLengthCluster(t *testing.T) {
	b1 := buildBlock(1, 0, true, []byte("VP9FRAME1"))
	b2 := buildBlock(2, 20, false, []byte("OPUSPACKET"))
	stream := buildWebM(false, b1, b2)

	seg, err := OpenSegment(bytes.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	var clusters []*Cluster
	err = seg.ScanClusters(func(c *Cluster) error {
		clusters = append(clusters, c)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(clusters) != 1 {
		t.Fatalf("want 1 cluster, got %d", len(clusters))
	}
	cl := clusters[0]
	if cl.Timestamp != 100 || len(cl.Blocks) != 2 {
		t.Fatalf("unexpected cluster: ts=%d blocks=%d", cl.Timestamp, len(cl.Blocks))
	}
	blk0 := cl.Blocks[0]
	if blk0.Track != 1 || blk0.RelTimecode != 0 || !blk0.Keyframe {
		t.Fatalf("block0 header wrong: %+v", blk0)
	}
	if got := blk0.AbsTimecode(cl.Timestamp); got != 100 {
		t.Fatalf("abs timecode=%d want 100", got)
	}
	if string(readAt(t, stream, blk0.PayloadStart, blk0.PayloadEnd)) != "VP9FRAME1" {
		t.Fatal("block0 codec payload interval wrong")
	}
	blk1 := cl.Blocks[1]
	if blk1.Track != 2 || blk1.Keyframe || blk1.AbsTimecode(100) != 120 {
		t.Fatalf("block1 header wrong: %+v", blk1)
	}
	if string(readAt(t, stream, blk1.PayloadStart, blk1.PayloadEnd)) != "OPUSPACKET" {
		t.Fatal("block1 codec payload interval wrong")
	}
}

func TestScanUnknownLengthSegmentAndCluster(t *testing.T) {
	b1 := buildBlock(1, 0, true, []byte("XYZ"))
	stream := buildWebM(true, b1)
	seg, err := OpenSegment(bytes.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if seg.element.Size != UnknownSize {
		t.Fatalf("segment size should be unknown, got %d", seg.element.Size)
	}
	var found int
	err = seg.ScanClusters(func(c *Cluster) error {
		found++
		if c.End != UnknownSize {
			t.Errorf("cluster end should be unknown, got %d", c.End)
		}
		if len(c.Blocks) != 1 || c.Blocks[0].Track != 1 {
			t.Errorf("block scan wrong: %+v", c.Blocks)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found != 1 {
		t.Fatalf("want 1 cluster, got %d", found)
	}
}

func TestTruncatedClusterReportedWithOffset(t *testing.T) {
	b1 := buildBlock(1, 0, true, make([]byte, 32))
	stream := buildWebM(true, b1)
	// Cut inside the block's codec payload. The unknown-length cluster has
	// no declared end, so the failure must surface while reading the payload
	// rather than at header validation.
	block := locateFirstBlock(t, stream)
	cut := block.PayloadStart + 5
	if cut >= int64(len(stream)) {
		t.Fatalf("bad cut point %d for %d-byte stream", cut, len(stream))
	}
	seg, err := OpenSegment(bytes.NewReader(stream[:cut]))
	if err != nil {
		t.Fatal(err)
	}
	err = seg.ScanClusters(func(c *Cluster) error { return nil })
	fe := fault.Of(err)
	if fe == nil {
		t.Fatal("expected structured fault for truncated cluster")
	}
	if fe.Kind != fault.KindTruncated || !fe.Recoverable {
		t.Fatalf("expected recoverable truncation, got %v", err)
	}
	// The reported offset must point at the first byte that could not be
	// read: exactly the cut position (±1 byte localization requirement).
	if delta := fe.Offset - cut; delta < -1 || delta > 1 {
		t.Fatalf("fault offset %d not within 1 byte of cut %d", fe.Offset, cut)
	}
}

// locateFirstBlock scans a valid stream and returns its first block, used to
// derive precise cut points for corruption fixtures.
func locateFirstBlock(t *testing.T, stream []byte) *Block {
	t.Helper()
	seg, err := OpenSegment(bytes.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	var found *Block
	if err := seg.ScanClusters(func(c *Cluster) error {
		if len(c.Blocks) > 0 {
			found = c.Blocks[0]
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if found == nil {
		t.Fatal("fixture contains no blocks")
	}
	return found
}

func TestBlockLengthOutOfRange(t *testing.T) {
	// Cluster with a block declaring 50 payload bytes but truncated to 10.
	body := append(elementHeader(IDTimestamp, 2), 0x00, 0x01)
	body = append(body, elementHeader(IDSimpleBlock, 50)...)
	body = append(body, buildBlock(1, 0, true, []byte("0123456789"))...)
	stream := append(elementHeader(IDEBMLHeader, 0),
		append(elementHeader(IDSegment, len(body)+len(elementHeader(IDCluster, len(body)))),
			append(elementHeader(IDCluster, len(body)), body...)...)...)
	seg, err := OpenSegment(bytes.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	err = seg.ScanClusters(func(c *Cluster) error { return nil })
	if !fault.Is(err, fault.KindLengthOutOfRange) {
		t.Fatalf("expected length-out-of-range fault, got %v", err)
	}
}

func TestLacedBlockRejected(t *testing.T) {
	// Flags 0x02 = Xiph lacing; otherwise a valid 8-byte block.
	raw := []byte{0x81, 0x00, 0x00, 0x02, 'a', 'b', 'c', 'd'}
	el := &Element{ID: IDSimpleBlock, Size: int64(len(raw)),
		HeaderEnd: 10, Start: 5, End: 10 + int64(len(raw))}
	r, err := NewReader(bytes.NewReader(append(make([]byte, 10), raw...)), 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseBlock(r, el, true); !fault.Is(err, fault.KindLengthOutOfRange) {
		t.Fatalf("expected lacing rejection, got %v", err)
	}
}

func TestBlockGroupBlockParsed(t *testing.T) {
	blk := buildBlock(3, -5, false, []byte("SUB"))
	groupBody := append(elementHeader(IDBlock, len(blk)), blk...)
	clusterBody := append(elementHeader(IDTimestamp, 1), 0x0A)
	clusterBody = append(clusterBody, elementHeader(IDBlockGroup, len(groupBody))...)
	clusterBody = append(clusterBody, groupBody...)
	stream := append(elementHeader(IDEBMLHeader, 0),
		append(elementHeader(IDSegment,
			len(elementHeader(IDCluster, len(clusterBody)))+len(clusterBody)),
			append(elementHeader(IDCluster, len(clusterBody)), clusterBody...)...)...)
	seg, err := OpenSegment(bytes.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	var got *Block
	err = seg.ScanClusters(func(c *Cluster) error {
		if len(c.Blocks) != 1 {
			t.Fatalf("want 1 block, got %d", len(c.Blocks))
		}
		got = c.Blocks[0]
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Track != 3 || got.RelTimecode != -5 || got.SimpleBlock {
		t.Fatalf("block group parse wrong: %+v", got)
	}
	if string(readAt(t, stream, got.PayloadStart, got.PayloadEnd)) != "SUB" {
		t.Fatal("codec payload interval wrong")
	}
}

func readAt(t *testing.T, buf []byte, start, end int64) []byte {
	t.Helper()
	if end > int64(len(buf)) || start < 0 || end < start {
		t.Fatalf("illegal range [%d,%d) in %d-byte buffer", start, end, len(buf))
	}
	return buf[start:end]
}
