package ebml

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"medtrace/internal/mederr"
)

// --- test-only EBML encoder helpers ----------------------------------------

func encVINTWidth(v uint64, width int, unknown bool) []byte {
	b := make([]byte, width)
	if unknown {
		for i := range b {
			b[i] = 0xFF
		}
		return b
	}
	for i := width - 1; i >= 1; i-- {
		b[i] = byte(v & 0xFF)
		v >>= 8
	}
	b[0] = byte(v) | (1 << uint(8-width))
	return b
}

// encVINT encodes v in the narrowest legal width.
func encVINT(v uint64) []byte {
	for w := 1; w <= 8; w++ {
		if v < (uint64(1)<<uint(7*w))-1 {
			return encVINTWidth(v, w, false)
		}
	}
	panic("vint too large")
}

// encID emits the canonical on-wire bytes of an element ID (marker included).
func encID(id ElementID) []byte {
	v := uint64(id)
	var w int
	switch {
	case v >= 0x10000000:
		w = 4
	case v >= 0x200000:
		w = 3
	case v >= 0x4000:
		w = 2
	default:
		w = 1
	}
	out := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		out[i] = byte(v)
		v >>= 8
	}
	return out[8-w:]
}

func encElem(id ElementID, body []byte) []byte {
	out := encID(id)
	out = append(out, encVINT(uint64(len(body)))...)
	return append(out, body...)
}

func encUnknown(id ElementID, body []byte) []byte {
	out := encID(id)
	out = append(out, encVINTWidth(0, 4, true)...)
	return append(out, body...)
}

func walkAll(t *testing.T, data []byte) []*Element {
	t.Helper()
	r := NewReader(bytes.NewReader(data))
	var got []*Element
	err := r.WalkChildren(nil, func(e *Element) error {
		got = append(got, e)
		return nil
	})
	if err != nil {
		t.Fatalf("WalkChildren: %v", err)
	}
	return got
}

// --- tests -----------------------------------------------------------------

func TestKnownLengthRoundTrip(t *testing.T) {
	data := encElem(IDDocType, []byte("webm"))
	got := walkAll(t, data)
	if len(got) != 1 || got[0].ID != IDDocType || got[0].UnknownSize {
		t.Fatalf("unexpected element %+v", got)
	}
	r := NewReader(bytes.NewReader(data))
	body, err := r.ReadBody(got[0])
	if err != nil || string(body) != "webm" {
		t.Fatalf("ReadBody body=%q err=%v", body, err)
	}
}

func TestUnknownLengthTerminatesAtParentBoundary(t *testing.T) {
	// Known-length Segment containing an unknown-length Info child; the Info
	// must consume exactly the rest of the segment, leaving no second child.
	info := encUnknown(IDInfo, encElem(IDMuxingApp, []byte("mt")))
	seg := encElem(IDSegment, info)
	got := walkAll(t, seg)
	if len(got) != 1 || got[0].ID != IDSegment || got[0].UnknownSize {
		t.Fatalf("unexpected top-level layout: %+v", got)
	}
	r := NewReader(bytes.NewReader(seg))
	if err := r.SeekTo(got[0].BodyOffset); err != nil {
		t.Fatal(err)
	}
	var inner []ElementID
	err := r.WalkChildren(got[0], func(e *Element) error {
		inner = append(inner, e.ID)
		if !e.UnknownSize {
			return r.SeekTo(e.End())
		}
		return nil
	})
	if err != nil || len(inner) != 1 || inner[0] != IDInfo {
		t.Fatalf("inner walk ids=%v err=%v", inner, err)
	}
	// Inside the unknown-length Info, exactly one MuxingApp before its EOF.
	if err := r.SeekTo(got[0].BodyOffset + int64(len(encID(IDInfo))+4)); err != nil {
		t.Fatal(err)
	}
	var leaf int
	infoElem := &Element{ID: IDInfo, UnknownSize: true, BodyOffset: got[0].BodyOffset}
	if err := r.WalkChildren(infoElem, func(e *Element) error { leaf++; return r.SeekTo(e.End()) }); err != nil {
		t.Fatal(err)
	}
	if leaf != 1 {
		t.Fatalf("unknown-length child must end at segment boundary, leaf=%d", leaf)
	}
}

func TestUnknownLengthAtEOFIsLegal(t *testing.T) {
	info := encElem(IDMuxingApp, []byte("mt"))
	seg := encUnknown(IDSegment, info) // segment runs to EOF
	got := walkAll(t, seg)
	if len(got) != 1 || !got[0].UnknownSize {
		t.Fatalf("expected single unknown segment, got %+v", got)
	}
}

func TestInvalidVINTZero(t *testing.T) {
	r := NewReader(bytes.NewReader([]byte{0x00, 0x82, 0x01, 0x00}))
	_, err := r.ReadElementHeader(-1)
	se := mederr.AsError(err)
	if se == nil || se.Category != mederr.CatInvalidVINT || se.Offset != 0 {
		t.Fatalf("want invalid_vint at 0, got %v", err)
	}
}

func TestReservedAllOnesID(t *testing.T) {
	r := NewReader(bytes.NewReader([]byte{0xFF, 0x80}))
	_, err := r.ReadElementHeader(-1)
	se := mederr.AsError(err)
	if se == nil || se.Category != mederr.CatInvalidVINT {
		t.Fatalf("want reserved-id error, got %v", err)
	}
}

func TestTruncatedHeader(t *testing.T) {
	// Valid 4-byte id VINT start, then stream ends inside the id itself.
	r := NewReader(bytes.NewReader([]byte{0x1A, 0x45}))
	_, err := r.ReadElementHeader(-1)
	se := mederr.AsError(err)
	if se == nil || se.Category != mederr.CatTruncatedHeader {
		t.Fatalf("want truncated_header, got %v", err)
	}
}

func TestLengthOutOfBounds(t *testing.T) {
	// Segment body claims a fixed length; corrupt the inner element size so
	// its declared body crosses the segment boundary.
	inner := encElem(IDInfo, make([]byte, 4))
	seg := encElem(IDSegment, inner)
	corrupt := make([]byte, len(seg))
	copy(corrupt, seg)
	segIDLen := len(encID(IDSegment))
	innerIDLen := len(encID(IDInfo))
	sizeAt := segIDLen + 1 + innerIDLen // segment size VINT is one byte
	corrupt[sizeAt] = 0xF0              // claims 112-byte body, crosses boundary
	r := NewReader(bytes.NewReader(corrupt))
	segBody := int64(segIDLen + 1)
	if err := r.SeekTo(segBody); err != nil {
		t.Fatal(err)
	}
	parent := &Element{ID: IDSegment, BodyOffset: segBody, Size: int64(len(inner))}
	err := r.WalkChildren(parent, func(e *Element) error { return nil })
	se := mederr.AsError(err)
	if se == nil || se.Category != mederr.CatLengthOutOfBounds {
		t.Fatalf("want length_out_of_bounds, got %v", err)
	}
	if se.Offset < 0 || !strings.Contains(se.Error(), "Segment") {
		t.Fatalf("error must carry offset and path: %v", se)
	}
}

func TestTruncatedBodyReported(t *testing.T) {
	// Header claims a 5-byte body, only 2 are present.
	data := encID(IDDocType)
	data = append(data, 0x85, 'a', 'b')
	r := NewReader(bytes.NewReader(data))
	e, err := r.ReadElementHeader(-1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadBody(e); mederr.AsError(err) == nil {
		t.Fatal("want truncated body error")
	}
}

func TestErrorCarriesElementPath(t *testing.T) {
	// Segment -> Info -> truncated VINT child (valid width marker, no body).
	bad := []byte{0x1A}
	info := encElem(IDInfo, bad)
	seg := encElem(IDSegment, info)
	r := NewReader(bytes.NewReader(seg))
	var seen string
	err := r.WalkChildren(nil, func(e *Element) error {
		if e.ID == IDSegment {
			return r.WalkChildren(e, func(inner *Element) error {
				if inner.ID == IDInfo {
					if err := r.SeekTo(inner.BodyOffset); err != nil {
						return err
					}
					return r.WalkChildren(inner, func(_ *Element) error { return nil })
				}
				return nil
			})
		}
		return nil
	})
	se := mederr.AsError(err)
	if se == nil || (se.Category != mederr.CatInvalidVINT && se.Category != mederr.CatTruncatedHeader) {
		t.Fatalf("want structured VINT error, got %v", err)
	}
	seen = strings.Join(se.Path, "/")
	if !strings.Contains(seen, "Segment") || !strings.Contains(seen, "Info") {
		t.Fatalf("path missing context: %q (%v)", seen, se)
	}
}

func TestParseBlock(t *testing.T) {
	// track VINT 0x81 (track 1), tc=0x000A, flags=0x80, payload 4 bytes.
	body := []byte{0x81, 0x00, 0x0A, 0x80, 1, 2, 3, 4}
	blk, err := ParseBlock(body)
	if err != nil {
		t.Fatal(err)
	}
	if blk.TrackNum != 1 || blk.Timecode != 10 || blk.Flags != 0x80 || len(blk.Payload) != 4 {
		t.Fatalf("bad block: %+v", blk)
	}
	if _, err := ParseBlock([]byte{0x81}); mederr.AsError(err) == nil {
		t.Fatal("want truncated block error")
	}
}

func TestEOFAtCleanBoundary(t *testing.T) {
	r := NewReader(bytes.NewReader(nil))
	if _, err := r.ReadElementHeader(-1); err != io.EOF {
		t.Fatalf("want io.EOF, got %v", err)
	}
}
