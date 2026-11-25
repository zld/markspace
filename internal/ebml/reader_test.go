package ebml

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"example.com/assetcheck/internal/timeline"
)

// --- fixture helpers -------------------------------------------------------

func idBytes(id uint32) []byte {
	switch {
	case id <= 0xFF:
		return []byte{byte(id)}
	case id <= 0xFFFF:
		return []byte{byte(id >> 8), byte(id)}
	case id <= 0xFFFFFF:
		return []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	default:
		return []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
	}
}

func sizeBytes(n int) []byte {
	switch {
	case n < 0x7F:
		return []byte{0x80 | byte(n)}
	case n < 0x3FFF:
		return []byte{0x40 | byte(n>>8), byte(n)}
	default:
		return []byte{0x20 | byte(n>>16), byte(n >> 8), byte(n)}
	}
}

// givenLen encodes a known-length element.
func givenLen(id uint32, payload []byte) []byte {
	out := append(idBytes(id), sizeBytes(len(payload))...)
	return append(out, payload...)
}

// unknownLen encodes an element with the all-ones size sentinel.
func unknownLen(id uint32) []byte {
	out := append([]byte{}, idBytes(id)...)
	out = append(out, 0xFF)
	return out
}

func uintElem(id uint32, v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	i := 0
	for i < 7 && b[i] == 0 {
		i++
	}
	return givenLen(id, b[i:])
}

func strElem(id uint32, s string) []byte { return givenLen(id, []byte(s)) }

func cat(t *testing.T, err error) timeline.Category {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	return timeline.CategoryOf(err)
}

func off(t *testing.T, err error) int64 {
	t.Helper()
	var te *timeline.Error
	if !errors.As(err, &te) {
		t.Fatalf("expected *timeline.Error, got %T: %v", err, err)
	}
	return te.Offset
}

func elemID(t *testing.T, err error) uint32 {
	t.Helper()
	var te *timeline.Error
	if !errors.As(err, &te) {
		t.Fatalf("expected *timeline.Error, got %T: %v", err, err)
	}
	return te.ElementID
}

func parse(t *testing.T, data []byte) (*Tree, error) {
	t.Helper()
	return ParseTree(bytes.NewReader(data), DefaultLimits())
}

func find(n *Node, id uint32) *Node {
	if n == nil {
		return nil
	}
	if n.ID == id {
		return n
	}
	for _, c := range n.Children {
		if got := find(c, id); got != nil {
			return got
		}
	}
	return nil
}

// segment builds a normal fixture: EBML header + Segment{Info, Tracks, Cluster}.
func fixture(clusterUnknownSize bool) []byte {
	info := givenLen(IDInfo, append(
		uintElem(IDTimecodeScale, 1_000_000),
		givenLen(IDMuxingApp, []byte("fixture"))...))
	tracks := givenLen(IDTracks, givenLen(IDTrackEntry,
		append(uintElem(IDTrackNumber, 1), uintElem(IDTrackType, 1)...)))
	block := givenLen(IDSimpleBlock, []byte{0x81, 0x00, 0x00, 0x00, 0xAA})
	var cluster []byte
	var segPayload []byte
	if clusterUnknownSize {
		cluster = append(unknownLen(IDCluster), givenLen(IDTimecode, []byte{0x00})...)
		cluster = append(cluster, block...)
		segPayload = append(info, tracks...)
		segPayload = append(segPayload, cluster...)
	} else {
		cluster = givenLen(IDCluster, append(givenLen(IDTimecode, []byte{0x00}), block...))
		segPayload = append(info, tracks...)
		segPayload = append(segPayload, cluster...)
	}
	header := givenLen(IDEBML, append(
		strElem(IDDocType, "matroska"),
		uintElem(IDDocTypeVersion, 4)...))
	out := append(header, unknownLen(IDSegment)...)
	return append(out, segPayload...)
}

// --- tests -----------------------------------------------------------------

func TestParseKnownLengthTreeAndOffsets(t *testing.T) {
	data := fixture(false)
	tree, err := parse(t, data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tree.Count != 14 {
		t.Fatalf("element count = %d, want 14", tree.Count)
	}
	seg := find(tree.Root, IDSegment)
	if seg == nil || !seg.Unknown {
		t.Fatalf("Segment should be an unknown-length master: %+v", seg)
	}
	if !seg.SizeKnown {
		if seg.Size != 0 {
			t.Fatalf("unknown-length size field = %d", seg.Size)
		}
	}
	if seg.Offset != int64(len(givenLen(IDEBML, append(
		strElem(IDDocType, "matroska"),
		uintElem(IDDocTypeVersion, 4)...)))) {
		t.Fatalf("Segment offset = %d", seg.Offset)
	}
	if seg.End != int64(len(data)) {
		t.Fatalf("Segment end = %d, want %d", seg.End, len(data))
	}
	block := find(tree.Root, IDSimpleBlock)
	if block == nil {
		t.Fatal("SimpleBlock not found")
	}
	if got := data[block.Offset : block.Offset+block.HeaderSize]; got[0] != 0xA3 {
		t.Fatalf("block offset %d does not point at 0xA3: %x", block.Offset, got)
	}
	// Offsets must point at the ID of every element.
	for _, n := range flatten(tree.Root) {
		if n.ID == 0 {
			continue
		}
		want := idBytes(n.ID)
		raw := data[n.Offset:]
		if len(raw) < len(want) || !bytes.Equal(raw[:len(want)], want) {
			t.Fatalf("offset %d for %s does not point at its ID", n.Offset, n.Name())
		}
	}
}

func flatten(n *Node) []*Node {
	out := []*Node{n}
	for _, c := range n.Children {
		out = append(out, flatten(c)...)
	}
	return out
}

func TestUnknownLengthClusterClosesAtSibling(t *testing.T) {
	data := fixture(true)
	tree, err := parse(t, data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cluster := find(tree.Root, IDCluster)
	if cluster == nil || !cluster.Unknown {
		t.Fatalf("Cluster should be unknown-length: %+v", cluster)
	}
	if cluster.End != int64(len(data)) {
		t.Fatalf("unknown-length cluster end = %d, want EOF %d", cluster.End, len(data))
	}
	if tree.Root.Child(IDSegment) == nil {
		t.Fatal("Segment missing")
	}
	// Adding a Cues element at segment level must close the cluster.
	cues := givenLen(IDCues, nil)
	data = append(data, cues...)
	tree, err = parse(t, data)
	if err != nil {
		t.Fatalf("parse with trailing Cues: %v", err)
	}
	cluster = find(tree.Root, IDCluster)
	if cluster == nil {
		t.Fatal("Cluster missing")
	}
	if cluster.End != int64(len(data)-len(cues)) {
		t.Fatalf("cluster end = %d, want %d (start of Cues)", cluster.End, len(data)-len(cues))
	}
	seg := tree.Root.Child(IDSegment)
	if c := find(seg, IDCues); c == nil {
		t.Fatal("Cues was not reattributed to Segment level")
	}
}

func TestUnknownLengthSegmentClosesAtNextTopLevel(t *testing.T) {
	header := givenLen(IDEBML, strElem(IDDocType, "matroska"))
	seg := append(unknownLen(IDSegment), givenLen(IDInfo, uintElem(IDTimecodeScale, 1_000_000))...)
	second := givenLen(IDEBML, strElem(IDDocType, "webm"))
	data := append(append(header, seg...), second...)
	tree, err := parse(t, data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	segs := tree.Root.ChildrenOf(IDSegment)
	if len(segs) != 1 {
		t.Fatalf("segment count = %d", len(segs))
	}
	if got := len(tree.Root.ChildrenOf(IDEBML)); got != 2 {
		t.Fatalf("EBML header count = %d; second header was swallowed by Segment", got)
	}
}

func TestUnknownSizeRejectedOutsideSegmentAndCluster(t *testing.T) {
	header := givenLen(IDEBML, strElem(IDDocType, "matroska"))
	data := append(header, unknownLen(IDSegment)...)
	data = append(data, unknownLen(IDTracks)...)
	_, err := parse(t, data)
	if got := cat(t, err); got != timeline.CatUnknownLength {
		t.Fatalf("category = %s, want %s", got, timeline.CatUnknownLength)
	}
	if got := elemID(t, err); got != IDTracks {
		t.Fatalf("element = 0x%X, want Tracks", got)
	}
	if got := off(t, err); got != int64(len(header)+len(unknownLen(IDSegment))) {
		t.Fatalf("offset = %d", got)
	}
}

func TestTruncatedElementReportsDeclaredOffset(t *testing.T) {
	data := fixture(false)
	cut := data[:len(data)-3]
	_, err := parse(t, cut)
	if got := cat(t, err); got != timeline.CatTruncated {
		t.Fatalf("category = %s, want %s (err=%v)", got, timeline.CatTruncated, err)
	}
	if got := elemID(t, err); got != IDSimpleBlock && got != IDCluster {
		t.Fatalf("element = 0x%X, want the block or its cluster", got)
	}
	if got := off(t, err); got < 0 || got >= int64(len(cut))+1 {
		t.Fatalf("offset = %d outside input", got)
	}
}

func TestIllegalFiveByteElementID(t *testing.T) {
	data := []byte{0x08, 0x00, 0x00, 0x00, 0x01, 0x81, 0x00}
	_, err := parse(t, data)
	if got := cat(t, err); got != timeline.CatIllegalLength {
		t.Fatalf("category = %s, want %s", got, timeline.CatIllegalLength)
	}
	if got := off(t, err); got != 0 {
		t.Fatalf("offset = %d, want 0", got)
	}
}

func TestChildOverrunningParentIsIllegalLength(t *testing.T) {
	// Cluster declares 3 payload bytes but its Timecode child needs 3 header + 1 payload.
	cluster := append(idBytes(IDCluster), sizeBytes(2)...)
	cluster = append(cluster, givenLen(IDTimecode, []byte{0})...)
	header := givenLen(IDEBML, strElem(IDDocType, "matroska"))
	data := append(header, unknownLen(IDSegment)...)
	data = append(data, givenLen(IDInfo, nil)...)
	data = append(data, cluster...)
	_, err := parse(t, data)
	if got := cat(t, err); got != timeline.CatIllegalLength {
		t.Fatalf("category = %s, want %s (err=%v)", got, timeline.CatIllegalLength, err)
	}
	if got := elemID(t, err); got != IDTimecode {
		t.Fatalf("element = 0x%X, want Timecode", got)
	}
}

func TestZeroLengthMarkerIsIllegal(t *testing.T) {
	data := append(givenLen(IDEBML, strElem(IDDocType, "matroska")), 0x00)
	_, err := parse(t, data)
	if got := cat(t, err); got != timeline.CatIllegalLength {
		t.Fatalf("category = %s, want %s", got, timeline.CatIllegalLength)
	}
}

func TestElementCountLimit(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxElements = 3
	header := givenLen(IDEBML, strElem(IDDocType, "matroska"))
	data := append(header, unknownLen(IDSegment)...)
	for i := 0; i < 5; i++ {
		data = append(data, givenLen(IDVoid, nil)...)
	}
	_, err := ParseTree(bytes.NewReader(data), lim)
	if got := cat(t, err); got != timeline.CatLimitExceeded {
		t.Fatalf("category = %s, want %s", got, timeline.CatLimitExceeded)
	}
}

func TestOversizedElementLimit(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxElementSize = 4
	header := givenLen(IDEBML, strElem(IDDocType, "matroska"))
	data := append(header, unknownLen(IDSegment)...)
	data = append(data, givenLen(IDVoid, make([]byte, 8))...)
	_, err := ParseTree(bytes.NewReader(data), lim)
	if got := cat(t, err); got != timeline.CatLimitExceeded {
		t.Fatalf("category = %s, want %s", got, timeline.CatLimitExceeded)
	}
}

func TestCollectFragmentsKeepsInterleavedOrder(t *testing.T) {
	// Two clusters, blocks interleaved: V,A,V,A.
	block := func(track byte) []byte {
		return givenLen(IDSimpleBlock, []byte{0x80 | track, 0x00, 0x01, 0x80})
	}
	c1 := givenLen(IDCluster, append(append(givenLen(IDTimecode, []byte{0}),
		block(1)...), block(2)...))
	c2raw := givenLen(IDTimecode, []byte{0x10})
	c2raw = append(c2raw, block(1)...)
	c2raw = append(c2raw, block(2)...)
	c2 := givenLen(IDCluster, c2raw)
	header := givenLen(IDEBML, strElem(IDDocType, "matroska"))
	data := append(header, unknownLen(IDSegment)...)
	data = append(data, c1...)
	data = append(data, c2...)
	tree, err := parse(t, data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	frags, err := CollectFragments(bytes.NewReader(data), tree)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(frags) != 4 {
		t.Fatalf("fragments = %d", len(frags))
	}
	wantTracks := []uint64{1, 2, 1, 2}
	for i, f := range frags {
		if f.TrackNumber != wantTracks[i] {
			t.Fatalf("fragment %d track = %d, want %d", i, f.TrackNumber, wantTracks[i])
		}
		if i > 0 && f.Order <= frags[i-1].Order && f.ClusterIndex == frags[i-1].ClusterIndex {
			t.Fatalf("fragment %d order not increasing", i)
		}
		if f.Offset <= 0 || data[f.Offset] != 0xA3 {
			t.Fatalf("fragment %d offset %d is wrong", i, f.Offset)
		}
	}
	if frags[0].Timecode != 1 || frags[3].Timecode != 1 {
		t.Fatalf("relative timecodes = %d,%d want 1,1", frags[0].Timecode, frags[3].Timecode)
	}
	if frags[2].ClusterTime != 16 {
		t.Fatalf("cluster time = %d", frags[2].ClusterTime)
	}
}

func TestReadPayloadRestoresCursor(t *testing.T) {
	docType := strElem(IDDocType, "matroska")
	data := givenLen(IDEBML, append(docType, givenLen(IDVoid, nil)...))
	r, err := NewReader(bytes.NewReader(data), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	head, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Descend(head); err != nil {
		t.Fatal(err)
	}
	el, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.ReadPayload(el, 32)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "matroska" {
		t.Fatalf("payload = %q", b)
	}
	if _, err := r.Next(); err != nil && err != io.EOF {
		t.Fatalf("next after payload: %v", err)
	}
}
