package ebml

import (
	"testing"

	"continuity/internal/cerr"
)

// build assembles elements: id followed by size-prefixed payload.
func build(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func elemT(id uint32, payload []byte) []byte {
	var out []byte
	switch {
	case id > 0xFFFFFF:
		out = []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
	case id > 0xFFFF:
		out = []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	case id > 0xFF:
		out = []byte{byte(id >> 8), byte(id)}
	default:
		out = []byte{byte(id)}
	}
	n := len(payload)
	switch {
	case n < 127:
		out = append(out, 0x80|byte(n))
	case n < 16383:
		out = append(out, 0x40|byte(n>>8), byte(n))
	default:
		out = append(out, 0x20|byte(n>>16), byte(n>>8), byte(n))
	}
	return append(out, payload...)
}

func paths(t *testing.T, data []byte) []string {
	t.Helper()
	var got []string
	err := New(data).Walk(func(el Element, _ []byte) (bool, error) {
		got = append(got, el.Path)
		return true, nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return got
}

func TestWalkValidDocument(t *testing.T) {
	doc := build(
		elemT(IDEBML, elemT(IDEBMLVersion, []byte{1})),
		elemT(IDSegment, build(
			elemT(IDInfo, elemT(IDTimecodeScale, []byte{0x0F, 0x42, 0x40})),
			elemT(IDCluster, elemT(IDTimecode, []byte{0})),
		)),
	)
	got := paths(t, doc)
	want := []string{
		"/EBML", "/EBML/EBMLVersion",
		"/Segment", "/Segment/Info", "/Segment/Info/TimecodeScale",
		"/Segment/Cluster", "/Segment/Cluster/Timecode",
	}
	if len(got) != len(want) {
		t.Fatalf("paths %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("path %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestUnknownSizeTerminatesAtParentBoundary(t *testing.T) {
	// Unknown-size Cluster inside a known-size Segment.
	cluster := append([]byte{0x1F, 0x43, 0xB6, 0x75, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF},
		elemT(IDTimecode, []byte{0})...)
	doc := build(elemT(IDSegment, cluster))
	got := paths(t, doc)
	want := []string{"/Segment", "/Segment/Cluster", "/Segment/Cluster/Timecode"}
	if len(got) != len(want) {
		t.Fatalf("paths %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("path %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestUnknownSizeRejectedOnNonMaster(t *testing.T) {
	// SimpleBlock (non-master) with unknown size.
	doc := build(elemT(IDSegment, []byte{
		0xA3, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
	}))
	err := New(doc).Walk(func(Element, []byte) (bool, error) { return true, nil })
	if !cerr.Is(err, cerr.CodeUnknownSizeBounds) {
		t.Fatalf("err = %v, want %s", err, cerr.CodeUnknownSizeBounds)
	}
}

func TestTruncatedHeader(t *testing.T) {
	doc := []byte{0x1A, 0x45} // EBML ID cut short
	err := New(doc).Walk(func(Element, []byte) (bool, error) { return true, nil })
	if !cerr.Is(err, cerr.CodeTruncatedHeader) {
		t.Fatalf("err = %v, want %s", err, cerr.CodeTruncatedHeader)
	}
	var ce *cerr.Error
	if cerr.Is(err, cerr.CodeTruncatedHeader) {
		ce = err.(*cerr.Error)
		if ce.Offset != 0 {
			t.Fatalf("offset = %d, want 0", ce.Offset)
		}
	}
}

func TestTruncatedData(t *testing.T) {
	// Unknown-size Segment (ends at EOF) so the truncation is located
	// at the child element, not the Segment header.
	doc := build(
		[]byte{0x18, 0x53, 0x80, 0x67, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF},
		elemT(IDTimecodeScale, []byte{1, 2, 3, 4}),
	)
	doc = doc[:len(doc)-2] // cut inside TimecodeScale data
	err := New(doc).Walk(func(Element, []byte) (bool, error) { return true, nil })
	if !cerr.Is(err, cerr.CodeTruncatedData) {
		t.Fatalf("err = %v, want %s", err, cerr.CodeTruncatedData)
	}
	ce := err.(*cerr.Error)
	if ce.Path != "/Segment/TimecodeScale" {
		t.Fatalf("path = %q", ce.Path)
	}
}

func TestSizeOverflowBeyondParent(t *testing.T) {
	// Segment declares 4 bytes; child TimecodeScale declares 10.
	// Trailing bytes keep the parent boundary inside the buffer so the
	// child overrun is a boundary violation, not a truncation.
	doc := build(elemT(IDSegment, []byte{0x2A, 0xD7, 0xB1, 0x8A}), make([]byte, 16))
	err := New(doc).Walk(func(Element, []byte) (bool, error) { return true, nil })
	if !cerr.Is(err, cerr.CodeSizeOverflow) {
		t.Fatalf("err = %v, want %s", err, cerr.CodeSizeOverflow)
	}
}

func TestInvalidVINT(t *testing.T) {
	doc := []byte{0x00, 0x80} // zero first byte: invalid VINT
	err := New(doc).Walk(func(Element, []byte) (bool, error) { return true, nil })
	if !cerr.Is(err, cerr.CodeVINTInvalid) {
		t.Fatalf("err = %v, want %s", err, cerr.CodeVINTInvalid)
	}
}

func TestVINTValue(t *testing.T) {
	v, n, err := VINTValue([]byte{0x82, 0x00})
	if err != nil || v != 2 || n != 1 {
		t.Fatalf("v=%d n=%d err=%v", v, n, err)
	}
	v, n, err = VINTValue([]byte{0x40, 0x85})
	if err != nil || v != 0x85 || n != 2 {
		t.Fatalf("v=%d n=%d err=%v", v, n, err)
	}
}
