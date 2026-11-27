package ebml

import (
	"bytes"
	"testing"

	"github.com/local/tracecheck/internal/clock"
)

const idDummy uint32 = 0xEC

func TestVINTRoundTrip(t *testing.T) {
	cases := []uint64{0, 1, 42, 126, 127, 128, 16382, 16383, 1 << 28}
	for _, v := range cases {
		raw := EncodeVINT(v)
		got, width, _, err := NewBytesReader(raw).readVINT(8, false)
		if err != nil {
			t.Fatalf("value %d: decode error %v", v, err)
		}
		if got != v || width != len(raw) {
			t.Fatalf("value %d: got %d width %d, want %d/%d", v, got, width, v, len(raw))
		}
	}
}

func TestIllegalAllOnesVINT(t *testing.T) {
	for _, raw := range [][]byte{{0xFF}, {0x7F, 0xFF}, {0x3F, 0xFF, 0xFF}} {
		_, _, _, err := NewBytesReader(raw).readVINT(8, false)
		te := clock.AsTraceError(err)
		if te == nil || te.Code != clock.CodeIllegalVINT {
			t.Fatalf("raw % X: expected illegal VINT, got %v", raw, err)
		}
	}
}

func TestTruncatedVINT(t *testing.T) {
	// Two-byte VINT marker with no following byte.
	_, _, _, err := NewBytesReader([]byte{0x42}).readVINT(8, false)
	if err == nil {
		t.Fatal("expected truncation error")
	}
}

func TestWalkKnownLength(t *testing.T) {
	doc := append(
		EncodeElement(idEBMLHeader, EncodeElement(idDummy, []byte{1, 2})),
		EncodeElement(idSegment, EncodeElement(idCluster, EncodeElement(idDummy, []byte{9})))...,
	)
	doc = append(doc, EncodeElement(idDummy, []byte{7})...)
	var ids []uint32
	err := NewBytesReader(doc).Walk(nil, func(el *Element, depth int) error {
		ids = append(ids, el.ID)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []uint32{idEBMLHeader, idDummy, idSegment, idCluster, idDummy, idDummy}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
	}
}

func TestUnknownLengthLegalAtEnd(t *testing.T) {
	// Segment of known length containing one known child followed by an
	// unknown-length master whose payload (a valid leaf element) reaches the
	// segment boundary.
	inner := EncodeElement(idTimecode, []byte{1})
	inner = append(inner, EncodeUnknownElement(idCluster, 1,
		EncodeElement(idDummy, []byte{2, 3, 4}))...)
	doc := EncodeElement(idSegment, inner)
	seen := map[uint32]int64{}
	err := NewBytesReader(doc).Walk(nil, func(el *Element, depth int) error {
		seen[el.ID] = el.Size
		return nil
	})
	if err != nil {
		t.Fatalf("legal unknown-length element rejected: %v", err)
	}
	if seen[idCluster] != -1 {
		t.Fatalf("cluster size = %d, want -1", seen[idCluster])
	}
}

func TestUnknownLengthTruncatedChild(t *testing.T) {
	// Unknown-length master whose payload ends in the middle of a child
	// header: the child's size VINT runs past the enclosing boundary.
	inner := append(EncodeID(idDummy), EncodeVINT(5)...)
	inner = append(inner, 1, 2) // only 2 of the declared 5 payload bytes
	doc := EncodeUnknownElement(idSegment, 1, inner)
	err := NewBytesReader(doc).Walk(nil, func(el *Element, depth int) error {
		return nil
	})
	te := clock.AsTraceError(err)
	if te == nil || te.Code != clock.CodeTruncatedElement {
		t.Fatalf("expected truncation inside unknown-length master, got %v", err)
	}
	if te.Offset <= 0 || te.Path == "" {
		t.Fatalf("error lacks offset/path: %+v", te)
	}
}

func TestUnknownLengthNoBoundary(t *testing.T) {
	// An open-ended stream (size -1) containing an unknown-length master
	// nested inside another unknown-length master cannot resolve a boundary.
	inner := EncodeUnknownElement(idCluster, 1, EncodeElement(idDummy, []byte{1}))
	doc := EncodeUnknownElement(idSegment, 1, inner)
	err := NewReader(bytes.NewReader(doc), -1).Walk(nil, func(el *Element, depth int) error {
		return nil
	})
	te := clock.AsTraceError(err)
	if te == nil || te.Code != clock.CodeBoundary {
		t.Fatalf("expected boundary error, got %v", err)
	}
}

func TestPayloadPastEOF(t *testing.T) {
	// Element declares 10 payload bytes but only 2 exist.
	doc := append(EncodeID(idDummy), EncodeVINT(10)...)
	doc = append(doc, 1, 2)
	err := NewBytesReader(doc).Walk(nil, func(el *Element, depth int) error {
		return nil
	})
	te := clock.AsTraceError(err)
	if te == nil || te.Code != clock.CodeTruncatedElement {
		t.Fatalf("expected truncation, got %v", err)
	}
}

func TestPayloadPastParent(t *testing.T) {
	// Hand-build a child whose end exceeds the segment boundary.
	seg := EncodeElement(idDummy, []byte{1})
	child := append(EncodeID(idCluster), EncodeVINT(50)...)
	doc := EncodeElement(idSegment, append(seg, child...))
	err := NewBytesReader(doc).Walk(nil, func(el *Element, depth int) error {
		return nil
	})
	te := clock.AsTraceError(err)
	if te == nil || te.Code != clock.CodeTruncatedElement {
		t.Fatalf("expected parent-boundary truncation, got %v", err)
	}
}

func TestPathRendering(t *testing.T) {
	doc := EncodeElement(idSegment, EncodeElement(idCluster, EncodeElement(idDummy, nil)))
	var path string
	err := NewBytesReader(doc).Walk(nil, func(el *Element, depth int) error {
		if el.ID == idDummy {
			path = el.Path()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/18538067/1F43B675/EC" {
		t.Fatalf("path = %q", path)
	}
}

func TestIntegerDecoders(t *testing.T) {
	if v, _ := Uint([]byte{0x01, 0x02}); v != 258 {
		t.Fatal("uint decode wrong")
	}
	if v, _ := SInt([]byte{0xFF}); v != -1 {
		t.Fatal("sint decode wrong")
	}
	if _, err := Uint(make([]byte, 9)); clock.AsTraceError(err) == nil {
		t.Fatal("oversized uint must error")
	}
}
