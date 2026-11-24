package ebml

import (
	"bytes"
	"io"
	"testing"

	"mediacheck/timeline"
)

// buildNested wraps payload in depth nested containers, all known size.
func buildNested(t *testing.T, depth int, leaf []byte) []byte {
	t.Helper()
	cur := leaf
	for i := 0; i < depth; i++ {
		b, err := BuildElement(ElementID(0x18538067), cur) // Segment ID
		if err != nil {
			t.Fatal(err)
		}
		cur = b
	}
	return cur
}

func TestRoundTripKnownSize(t *testing.T) {
	payload := []byte{1, 2, 3, 4, 5}
	raw, err := BuildElement(ElementID(0x1F43B675), payload) // Cluster ID
	if err != nil {
		t.Fatal(err)
	}
	r := NewReader(bytes.NewReader(raw))
	h, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if h.ID != ElementID(0x1F43B675) || h.Size != int64(len(payload)) || h.Offset != 0 {
		t.Fatalf("bad header %+v", h)
	}
	got, err := r.ReadPayload(h)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch %v %v", got, err)
	}
	if _, err := r.Next(); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestVINTEncodeSizes(t *testing.T) {
	cases := []struct {
		size uint64
		want int
	}{
		{0, 1}, {63, 1}, {64, 2}, {8191, 2}, {8192, 3},
	}
	for _, c := range cases {
		buf, err := EncodeSize(c.size, 0)
		if err != nil || len(buf) != c.want {
			t.Fatalf("size %d -> len %d err %v want %d", c.size, len(buf), err, c.want)
		}
		v, unknown, err := decodeVINT(buf, false)
		if err != nil || unknown || v != c.size {
			t.Fatalf("decode %d -> %d unknown=%v err=%v", c.size, v, unknown, err)
		}
	}
	unk, err := EncodeUnknownSize(4)
	if err != nil {
		t.Fatal(err)
	}
	if _, unknown, err := decodeVINT(unk, false); err != nil || !unknown {
		t.Fatalf("unknown size not recognized: %v %v", unknown, err)
	}
}

func TestUnknownLengthContainer(t *testing.T) {
	// Segment with unknown size, two Cluster children with known sizes.
	var buf bytes.Buffer
	buf.Write(EncodeID(0x18538067))
	us, err := EncodeUnknownSize(8)
	if err != nil {
		t.Fatal(err)
	}
	buf.Write(us)
	child, err := BuildElement(ElementID(0x1F43B675), []byte{0xAA})
	if err != nil {
		t.Fatal(err)
	}
	buf.Write(child)
	buf.Write(child)

	r := NewReader(bytes.NewReader(buf.Bytes()))
	h, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if h.Size != UnknownSize {
		t.Fatalf("want unknown size, got %d", h.Size)
	}
	if err := r.Enter(h); err != nil {
		t.Fatal(err)
	}
	count := 0
	for {
		ch, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.ReadPayload(ch); err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != 2 {
		t.Fatalf("got %d children, want 2", count)
	}
	if err := r.Exit(); err != nil {
		t.Fatal(err)
	}
}

func TestTruncatedElementReportsOffset(t *testing.T) {
	// Header claims 10 payload bytes, only 3 present.
	var buf bytes.Buffer
	buf.Write(EncodeID(0x1F43B675))
	sz, _ := EncodeSize(10, 0)
	buf.Write(sz)
	buf.Write([]byte{1, 2, 3})
	r := NewReader(bytes.NewReader(buf.Bytes()))
	h, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadPayload(h); !timeline.IsCategory(err, timeline.CatCorrupt) {
		t.Fatalf("want corrupt error, got %v", err)
	}
	// Truncated header mid-stream.
	r2 := NewReader(bytes.NewReader([]byte{0x42, 0x82}))
	if _, err := r2.Next(); !timeline.IsCategory(err, timeline.CatCorrupt) {
		t.Fatalf("want corrupt header error, got %v", err)
	}
}

func TestDepthLimit(t *testing.T) {
	raw := buildNested(t, MaxDepth+1, []byte{0x10})
	r := NewReader(bytes.NewReader(raw))
	var walk func(depth int) error
	walk = func(depth int) error {
		h, err := r.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := r.Enter(h); err != nil {
			return err
		}
		defer r.Exit()
		return walk(depth + 1)
	}
	err := walk(0)
	if !timeline.IsCategory(err, timeline.CatCorrupt) {
		t.Fatalf("want depth-limit corrupt error, got %v", err)
	}
}

func TestOversizeElementRejected(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(EncodeID(0x1F43B675))
	big, err := EncodeSize(uint64(MaxElementPayload)+1, 8)
	if err != nil {
		t.Fatal(err)
	}
	buf.Write(big)
	r := NewReader(bytes.NewReader(buf.Bytes()))
	if _, err := r.Next(); !timeline.IsCategory(err, timeline.CatOverflow) {
		t.Fatalf("want overflow error, got %v", err)
	}
}
