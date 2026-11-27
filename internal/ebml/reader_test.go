package ebml

import (
	"testing"

	"mediad/internal/clock"
)

// encID appends a big-endian element ID of the minimal legal length.
func encID(buf []byte, id uint32) []byte {
	switch {
	case id > 0xFFFFFF:
		return append(buf, byte(id>>24), byte(id>>16), byte(id>>8), byte(id))
	case id > 0xFFFF:
		return append(buf, byte(id>>16), byte(id>>8), byte(id))
	case id > 0xFF:
		return append(buf, byte(id>>8), byte(id))
	default:
		return append(buf, byte(id))
	}
}

// encSize appends a 1-byte size for small payloads.
func encSize(buf []byte, size int) []byte {
	return append(buf, 0x80|byte(size))
}

func encUnknown(buf []byte) []byte { return append(buf, 0xFF) }

// elem builds a complete known-size element.
func elem(id uint32, payload []byte) []byte {
	buf := encID(nil, id)
	buf = encSize(buf, len(payload))
	return append(buf, payload...)
}

func uintPayload(v uint64) []byte {
	var b [8]byte
	n := 8
	for v > 0 {
		n--
		b[n] = byte(v)
		v >>= 8
	}
	if n == 8 {
		n = 7
	}
	return b[n:]
}

// validDoc builds: EBML header, unknown-size Segment containing Info and one
// known-size Cluster with a Timecode.
func validDoc() []byte {
	var doc []byte
	doc = append(doc, elem(IDEBML, []byte{0x42, 0x86, 0x81, 0x01})...)
	var seg []byte
	seg = append(seg, elem(IDInfo, elem(IDTimecodeScale, uintPayload(1_000_000)))...)
	var clu []byte
	clu = append(clu, elem(IDTimecode, uintPayload(0))...)
	seg = append(seg, elem(IDCluster, clu)...)
	doc = append(doc, encID(nil, IDSegment)...)
	doc = encUnknown(doc)
	return append(doc, seg...)
}

func TestParseValidUnknownSizeSegment(t *testing.T) {
	nodes, err := NewParser(validDoc()).Parse()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 || nodes[1].ID != IDSegment {
		t.Fatalf("unexpected top level: %+v", nodes)
	}
	seg := nodes[1]
	if seg.Size != UnknownSize {
		t.Fatalf("segment size = %d, want unknown", seg.Size)
	}
	info := seg.Find(IDInfo)
	if info == nil {
		t.Fatal("missing Info")
	}
	ts := info.Find(IDTimecodeScale)
	v, err := ts.Uint()
	if err != nil || v != 1_000_000 {
		t.Fatalf("timecode scale = %d, %v", v, err)
	}
	clu := seg.Find(IDCluster)
	if clu == nil || clu.Find(IDTimecode) == nil {
		t.Fatal("missing Cluster/Timecode")
	}
}

func TestUnknownSizeClusterEndsAtSiblingBoundary(t *testing.T) {
	var seg []byte
	// Unknown-size cluster containing only a Timecode, then a second cluster.
	var c1 []byte
	c1 = append(c1, elem(IDTimecode, uintPayload(0))...)
	seg = append(seg, encID(nil, IDCluster)...)
	seg = encUnknown(seg)
	seg = append(seg, c1...)
	seg = append(seg, elem(IDCluster, elem(IDTimecode, uintPayload(40)))...)
	doc := append(elem(IDEBML, []byte{0x01}), elem(IDSegment, seg)...)

	nodes, err := NewParser(doc).Parse()
	if err != nil {
		t.Fatal(err)
	}
	clusters := nodes[1].FindAll(IDCluster)
	if len(clusters) != 2 {
		t.Fatalf("got %d clusters, want 2", len(clusters))
	}
	tc, _ := clusters[1].Find(IDTimecode).Uint()
	if tc != 40 {
		t.Fatalf("second cluster timecode = %d", tc)
	}
}

func TestInvalidVINT(t *testing.T) {
	doc := append(elem(IDEBML, []byte{0x01}), 0x00, 0x80)
	_, err := NewParser(doc).Parse()
	e, ok := err.(*clock.Error)
	if !ok || e.Code != clock.ErrInvalidVINT {
		t.Fatalf("want ERR_INVALID_VINT, got %v", err)
	}
	if e.Offset != int64(len(doc)-2) {
		t.Fatalf("offset = %d", e.Offset)
	}
}

func TestTruncatedHeader(t *testing.T) {
	doc := validDoc()
	doc = doc[:len(doc)-2] // cut inside the last element header/payload
	_, err := NewParser(doc).Parse()
	if clock.CodeOf(err) != clock.ErrTruncatedHeader && clock.CodeOf(err) != clock.ErrBounds {
		t.Fatalf("want truncation/bounds error, got %v", err)
	}
}

func TestOutOfBoundsSize(t *testing.T) {
	// Info declares a payload larger than the remaining segment.
	bad := encID(nil, IDInfo)
	bad = append(bad, 0x7F) // size 127, almost nothing follows
	bad = append(bad, 0x01, 0x02)
	doc := append(elem(IDEBML, []byte{0x01}), elem(IDSegment, bad)...)
	_, err := NewParser(doc).Parse()
	e, ok := err.(*clock.Error)
	if !ok || e.Code != clock.ErrBounds {
		t.Fatalf("want ERR_BOUNDS, got %v", err)
	}
	if e.Path == "" {
		t.Fatal("error must carry element path")
	}
}

func TestUnknownSizeOnLeafRejected(t *testing.T) {
	var doc []byte
	doc = append(doc, elem(IDEBML, []byte{0x01})...)
	doc = append(doc, encID(nil, IDSegment)...)
	doc = encUnknown(doc)
	doc = append(doc, encID(nil, IDTimecodeScale)...) // leaf, not master
	doc = encUnknown(doc)
	_, err := NewParser(doc).Parse()
	if clock.CodeOf(err) != clock.ErrUnknownSizeMisuse {
		t.Fatalf("want ERR_UNKNOWN_SIZE_MISUSE, got %v", err)
	}
}

func TestElementSizeCap(t *testing.T) {
	p := NewParser(elem(IDSegment, elem(IDInfo, make([]byte, 64))))
	p.MaxElementSize = 8
	_, err := p.Parse()
	if clock.CodeOf(err) != clock.ErrSizeOverflow {
		t.Fatalf("want ERR_SIZE_OVERFLOW, got %v", err)
	}
}
