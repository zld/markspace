package ebml

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// vint helpers mirror EBML encoders used by the fixture generator later.
func encVInt(value uint64, width int) []byte {
	marker := byte(1) << uint(8-width)
	out := make([]byte, width)
	v := value
	for i := width - 1; i >= 0; i-- {
		out[i] = byte(v)
		v >>= 8
	}
	out[0] |= marker
	return out
}

// encID emits an EBML element ID: its own leading marker bits are preserved.
func encID(id uint32, width int) []byte {
	out := make([]byte, width)
	v := id
	for i := width - 1; i >= 0; i-- {
		out[i] = byte(v)
		v >>= 8
	}
	return out
}

func encSize(size uint64) []byte {
	if size == unknownSizeMarker {
		return []byte{0x01, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	}
	for w := 1; w <= 8; w++ {
		limit := uint64(1) << uint(7*w)
		if size < limit-1 {
			return encVInt(size+limit, w)
		}
	}
	panic("size too large")
}

const unknownSizeMarker = ^uint64(0)

func elem(id uint32, idW int, body []byte) []byte {
	return append(append(encID(id, idW), encSize(uint64(len(body)))...), body...)
}

func TestReadVIntWidthsAndValues(t *testing.T) {
	cases := []struct {
		raw []byte
		v   uint64
	}{
		{[]byte{0x82}, 2},
		{[]byte{0x40, 0x06}, 6},
		{[]byte{0x20, 0x00, 0x09}, 9},
		{[]byte{0x10, 0x00, 0x00, 0x0c}, 12},
	}
	for _, c := range cases {
		r := NewReader(bytes.NewReader(c.raw), DefaultLimits())
		got, w, unk, err := r.ReadVInt(false)
		if err != nil || got != c.v || w != len(c.raw) || unk {
			t.Fatalf("ReadVInt(% X) = %d,w=%d,unk=%v,err=%v", c.raw, got, w, unk, err)
		}
	}
}

func TestIllegalWidthAndUnknownSize(t *testing.T) {
	r := NewReader(bytes.NewReader([]byte{0x00, 0x80}), DefaultLimits())
	_, _, _, err := r.ReadVInt(false)
	var ee *Error
	if !errors.As(err, &ee) || ee.Class != ErrBadWidth || ee.Offset != 0 {
		t.Fatalf("want BAD_VINT_WIDTH at 0, got %v", err)
	}

	unk := []byte{0x01, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	r2 := NewReader(bytes.NewReader(unk), DefaultLimits())
	v, w, isUnk, err := r2.ReadVInt(false)
	if err != nil || !isUnk || w != 8 || v != 0 {
		t.Fatalf("want unknown size, got v=%d w=%d unk=%v err=%v", v, w, isUnk, err)
	}
}

func TestTruncatedVIntAndPayload(t *testing.T) {
	// 2-byte VINT header promises another octet that never arrives.
	r := NewReader(bytes.NewReader([]byte{0x40}), DefaultLimits())
	_, _, _, err := r.ReadVInt(false)
	var ee *Error
	if !errors.As(err, &ee) || ee.Class != ErrTruncated || ee.Offset != 1 {
		t.Fatalf("want TRUNCATED at offset 1, got %v", err)
	}

	// Element declares 4 payload bytes but only 2 exist.
	doc := append(encID(0x1A45DFA3, 4), append(encSize(4), 0xAA, 0xBB)...)
	r2 := NewReader(bytes.NewReader(doc), DefaultLimits())
	hdr, err := r2.ReadElementHeader(0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r2.Discard(hdr.EndOff - r2.Offset()); !errors.As(err, &ee) || ee.Class != ErrTruncated {
		t.Fatalf("want payload TRUNCATED, got %v", err)
	}
	if ee.Offset != int64(len(doc)) {
		t.Fatalf("truncation offset %d, want file length %d", ee.Offset, len(doc))
	}
}

func TestBoundedNestedTreeAndOverrun(t *testing.T) {
	child := elem(0x4287, 2, []byte{0x01})
	parent := elem(0x18538067, 4, child)
	doc := append(elem(0x1A45DFA3, 4, []byte{1}), parent...)

	r := NewReader(bytes.NewReader(doc), DefaultLimits())
	hdr, err := r.ReadElementHeader(0, -1) // EBML header
	if err != nil || hdr.ID != 0x1A45DFA3 {
		t.Fatalf("ebml header = %v, %v", hdr, err)
	}
	if err := r.SkipElement(hdr); err != nil {
		t.Fatal(err)
	}
	root, err := r.ReadElementHeader(0, -1)
	if err != nil || root.ID != 0x18538067 {
		t.Fatalf("segment = %v, %v", root, err)
	}
	it := r.Children(root, -1)
	c, err := it.Next()
	if err != nil || c.ID != 0x4287 {
		t.Fatalf("child = %v, %v", c, err)
	}
	if err := r.SkipElement(c); err != nil {
		t.Fatal(err)
	}
	if _, err := it.Next(); err != io.EOF {
		t.Fatalf("want EOF at parent end, got %v", err)
	}
	if r.Offset() != root.EndOff {
		t.Fatalf("offset %d != parent end %d", r.Offset(), root.EndOff)
	}
}

func TestUnknownLengthParentTerminatesAtSibling(t *testing.T) {
	// Unknown-length Segment containing one child, then a top-level EBML sibling.
	segHead := append(encID(0x18538067, 4), encSize(unknownSizeMarker)...)
	child := elem(0x4287, 2, []byte{7})
	sibling := elem(0x1F43B675, 4, []byte{9})
	doc := append(append(segHead, child...), sibling...)

	r := NewReader(bytes.NewReader(doc), DefaultLimits())
	seg, err := r.ReadElementHeader(0, -1)
	if err != nil || !seg.UnknownSize {
		t.Fatalf("segment = %v, %v", seg, err)
	}
	// With a known hard end, the unknown-length parent ends at that boundary.
	hardEnd := int64(len(doc) - len(sibling))
	it := r.Children(seg, hardEnd)
	c, err := it.Next()
	if err != nil || c.ID != 0x4287 {
		t.Fatalf("unknown-len child = %v, %v", c, err)
	}
	if err := r.SkipElement(c); err != nil {
		t.Fatal(err)
	}
	if _, err := it.Next(); err != io.EOF {
		t.Fatalf("unknown parent must stop at hard end, got %v", err)
	}
	if r.Offset() != hardEnd {
		t.Fatalf("offset %d != hard end %d", r.Offset(), hardEnd)
	}
	next, err := r.ReadElementHeader(0, -1)
	if err != nil || next.ID != 0x1F43B675 {
		t.Fatalf("sibling lost after unknown parent: %v, %v", next, err)
	}
}

func TestUnknownLengthParentStopsAtSiblingID(t *testing.T) {
	// When no hard end exists, a registered sibling ID terminates the parent.
	segHead := append(encID(0x18538067, 4), encSize(unknownSizeMarker)...)
	child := elem(0x4287, 2, []byte{7})
	sibling := elem(0x1F43B675, 4, []byte{9})
	doc := append(append(segHead, child...), sibling...)

	r := NewReader(bytes.NewReader(doc), DefaultLimits())
	seg, err := r.ReadElementHeader(0, -1)
	if err != nil {
		t.Fatal(err)
	}
	it := r.Children(seg, -1, 0x1F43B675)
	c, err := it.Next()
	if err != nil || c.ID != 0x4287 {
		t.Fatalf("child = %v, %v", c, err)
	}
	if err := r.SkipElement(c); err != nil {
		t.Fatal(err)
	}
	if _, err := it.Next(); err != io.EOF {
		t.Fatalf("sibling ID must terminate unknown parent, got %v", err)
	}
	next, err := r.ReadElementHeader(0, -1)
	if err != nil || next.ID != 0x1F43B675 {
		t.Fatalf("sibling lost: %v, %v", next, err)
	}
}

func TestDepthAndSizeLimits(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxDepth = 1
	doc := elem(0x18538067, 4, elem(0x1F43B675, 4, []byte{0}))
	r := NewReader(bytes.NewReader(doc), lim)
	root, err := r.ReadElementHeader(0, -1)
	if err != nil {
		t.Fatal(err)
	}
	it := r.Children(root, -1)
	child, err := it.Next() // depth 1 is allowed
	if err != nil {
		t.Fatal(err)
	}
	cit := r.Children(child, root.EndOff)
	_, err = cit.Next() // depth 2 exceeds limit
	var ee *Error
	if !errors.As(err, &ee) || ee.Class != ErrNestedTooDeep {
		t.Fatalf("want NESTED_TOO_DEEP, got %v", err)
	}

	lim2 := DefaultLimits()
	lim2.MaxElementSz = 2
	big := append(append(encID(0x4287, 2), encSize(5)...), 1, 2, 3, 4, 5)
	r2 := NewReader(bytes.NewReader(big), lim2)
	_, err = r2.ReadElementHeader(0, -1)
	if !errors.As(err, &ee) || ee.Class != ErrOversized {
		t.Fatalf("want OVERSIZED, got %v", err)
	}
}
