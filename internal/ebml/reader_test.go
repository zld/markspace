package ebml

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// buildSegment serializes a Segment containing one Info child. When unknown
// is true the Segment uses the unknown-length marker and carries no size VINT.
func buildSegment(unknown bool) []byte {
	info := &MasterBuilder{}
	info.Uint(IDTimecodeScale, 1_000_000)
	payload := Build(IDInfo, info)
	if unknown {
		return append(EncodeID(IDSegment), append(EncodeUnknownSize(), payload...)...)
	}
	return EncodeElement(IDSegment, payload)
}

func TestRoundTripKnownLength(t *testing.T) {
	data := buildSegment(false)
	r := NewReader(bytes.NewReader(data))
	el, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if el.ID != IDSegment || el.Size != int64(len(data)-len(EncodeID(IDSegment))-1) {
		t.Fatalf("bad segment header: %+v", el)
	}
	if err := r.Enter(el); err != nil {
		t.Fatal(err)
	}
	child, err := r.Next()
	if err != nil {
		t.Fatalf("child Next: %v", err)
	}
	if child.ID != IDInfo {
		t.Fatalf("want Info, got %x", child.ID)
	}
	if err := r.Enter(child); err != nil {
		t.Fatal(err)
	}
	sc, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := r.ReadData(sc)
	if err != nil {
		t.Fatal(err)
	}
	v, err := Uint(raw)
	if err != nil || v != 1_000_000 {
		t.Fatalf("timecode scale = %d, err %v", v, err)
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF, got %v", err)
	}
	if err := r.Exit(); err != nil { // Info
		t.Fatal(err)
	}
	if err := r.Exit(); err != nil { // Segment
		t.Fatal(err)
	}
}

func TestUnknownLengthSegmentCleanEOF(t *testing.T) {
	// Segment with unknown size, Info child known length, physical EOF closes
	// the segment legally: Next must return io.EOF, not a truncation error.
	data := buildSegment(true)
	r := NewReader(bytes.NewReader(data))
	seg, err := r.Next()
	if err != nil || seg.Size != UnknownSize {
		t.Fatalf("seg: %+v err %v", seg, err)
	}
	if err := r.Enter(seg); err != nil {
		t.Fatal(err)
	}
	child, err := r.Next()
	if err != nil || child.ID != IDInfo {
		t.Fatalf("child: %v %v", child, err)
	}
	if err := r.Skip(child); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("unknown-length boundary must be clean EOF, got %v", err)
	}
}

func TestUnknownLengthNestedCluster(t *testing.T) {
	// Unknown Segment -> unknown Cluster -> known Timecode, then EOF.
	tc := EncodeElement(IDTimecode, EncodeUint(42))
	cluster := append(EncodeID(IDCluster), EncodeUnknownSize()...)
	cluster = append(cluster, tc...)
	seg := append(EncodeID(IDSegment), EncodeUnknownSize()...)
	seg = append(seg, cluster...)

	r := NewReader(bytes.NewReader(seg))
	segEl, _ := r.Next()
	if err := r.Enter(segEl); err != nil {
		t.Fatal(err)
	}
	cl, err := r.Next()
	if err != nil || cl.ID != IDCluster || cl.Size != UnknownSize {
		t.Fatalf("cluster: %+v err %v", cl, err)
	}
	if err := r.Enter(cl); err != nil {
		t.Fatal(err)
	}
	tcEl, err := r.Next()
	if err != nil || tcEl.ID != IDTimecode {
		t.Fatalf("timecode: %v %v", tcEl, err)
	}
	raw, err := r.ReadData(tcEl)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := Uint(raw); v != 42 {
		t.Fatalf("timecode = %d", v)
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("nested unknown boundary: want EOF, got %v", err)
	}
}

func TestTruncatedElementReportsOffset(t *testing.T) {
	// Known-length Segment containing a TimecodeScale whose 4-byte payload is
	// cut after 2 bytes: the header parses, ReadData must surface the offset.
	tc := EncodeElement(IDTimecodeScale, []byte{0x00, 0x0F, 0x42, 0x40})
	seg := EncodeElement(IDSegment, tc)
	headerLen := len(seg) - len(tc) + len(tc) - 2 // cut within the uint payload
	cut := seg[:headerLen]
	r := NewReader(bytes.NewReader(cut))
	segEl, err := r.Next()
	if err != nil {
		t.Fatalf("segment header: %v", err)
	}
	if err := r.Enter(segEl); err != nil {
		t.Fatal(err)
	}
	sc, err := r.Next()
	if err != nil || sc == nil {
		t.Fatalf("scalar header should parse, got el=%v err=%v", sc, err)
	}
	if sc.ID != IDTimecodeScale {
		t.Fatalf("unexpected id %x", sc.ID)
	}
	_, err = r.ReadData(sc)
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindTruncated {
		t.Fatalf("want KindTruncated, got %v", err)
	}
	if e.Offset != int64(len(cut)) {
		t.Fatalf("offset = %d, want %d", e.Offset, len(cut))
	}
	if e.ElemID != IDTimecodeScale {
		t.Fatalf("error must carry element id, got %x", e.ElemID)
	}
}

func TestTruncatedHeader(t *testing.T) {
	// A 4-byte ID with only its first byte present: the VINT promises more
	// bytes than the stream delivers, so this is truncation rather than EOF.
	r := NewReader(bytes.NewReader([]byte{0x1A}))
	_, err := r.Next()
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindTruncated {
		t.Fatalf("want truncated header, got %v", err)
	}
}

func TestCleanEOFAtTopLevel(t *testing.T) {
	// A bare physical EOF with no open container is a normal stream end.
	r := NewReader(bytes.NewReader(nil))
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("want io.EOF, got %v", err)
	}
}

func TestElementOverrunRejected(t *testing.T) {
	// Scalar inside Segment whose declared size extends past Segment end.
	inner := EncodeElement(IDTimecodeScale, make([]byte, 8))
	seg := EncodeElement(IDSegment, inner)
	// Shrink the segment's declared size by 2 without touching the child.
	seg[len(EncodeID(IDSegment))] = 0x80 | byte(len(inner)-2)
	r := NewReader(bytes.NewReader(seg))
	segEl, _ := r.Next()
	_ = r.Enter(segEl)
	_, err := r.Next()
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindTruncated {
		t.Fatalf("child overrun must be KindTruncated, got %v", err)
	}
}

func TestOversizeElementRejected(t *testing.T) {
	// Valid 1-byte ID followed by a 4-byte size VINT declaring a payload
	// larger than MaxElementSize.
	big := uint64(MaxElementSize) + 1
	sizeVINT := EncodeVINT(big, false)
	data := append(EncodeID(IDTimecode), sizeVINT...)
	r := NewReader(bytes.NewReader(data))
	_, err := r.Next()
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindOversize {
		t.Fatalf("want KindOversize, got %v", err)
	}
}

func TestDepthLimit(t *testing.T) {
	// Nest MaxDepth+1 unknown-length Info-like containers.
	var data []byte
	for i := 0; i < MaxDepth+1; i++ {
		data = append(data, EncodeID(IDInfo)...)
		data = append(data, EncodeUnknownSize()...)
	}
	r := NewReader(bytes.NewReader(data))
	var last error
	for i := 0; i <= MaxDepth; i++ {
		el, err := r.Next()
		if err != nil {
			last = err
			break
		}
		last = r.Enter(el)
		if last != nil {
			break
		}
	}
	var e *Error
	if !errors.As(last, &e) || e.Kind != KindOversize {
		t.Fatalf("want depth KindOversize, got %v", last)
	}
}

func TestSkipAndExit(t *testing.T) {
	info := &MasterBuilder{}
	info.Uint(IDTimecodeScale, 1_000_000)
	info.String(IDWritingApp, "mediasync")
	data := EncodeElement(IDInfo, info.BytesValue())
	r := NewReader(bytes.NewReader(data))
	el, _ := r.Next()
	if err := r.Enter(el); err != nil {
		t.Fatal(err)
	}
	first, _ := r.Next()
	if err := r.Skip(first); err != nil {
		t.Fatal(err)
	}
	second, err := r.Next()
	if err != nil || second.ID != IDWritingApp {
		t.Fatalf("second child: %v %v", second, err)
	}
	if err := r.Exit(); err != nil { // consumes writing app payload, closes Info
		t.Fatal(err)
	}
}
