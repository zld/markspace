package bmff

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func be32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func mkbox(typ string, payload []byte) []byte {
	out := append(be32(uint32(len(payload)+8)), typ...)
	return append(out, payload...)
}

func readerOf(t *testing.T, data []byte) *Reader {
	t.Helper()
	return NewReader(bytes.NewReader(data), int64(len(data)))
}

func collect(t *testing.T, r *Reader) []Box {
	t.Helper()
	var boxes []Box
	err := r.WalkTop(func(b Box) (bool, error) {
		boxes = append(boxes, b)
		return false, nil
	})
	if err != nil {
		t.Fatalf("WalkTop: %v", err)
	}
	return boxes
}

func TestWalkSimple(t *testing.T) {
	data := append(mkbox("ftyp", []byte("isom")), mkbox("free", nil)...)
	boxes := collect(t, readerOf(t, data))
	if len(boxes) != 2 || boxes[0].Type != "ftyp" || boxes[1].Type != "free" {
		t.Fatalf("unexpected boxes: %+v", boxes)
	}
	if boxes[0].DataSize() != 4 || boxes[1].DataSize() != 0 {
		t.Fatalf("bad sizes: %+v", boxes)
	}
}

func TestExtendedSize(t *testing.T) {
	payload := []byte("abcd")
	var b []byte
	b = append(b, be32(1)...)
	b = append(b, "wide"...)
	var ext [8]byte
	binary.BigEndian.PutUint64(ext[:], uint64(16+len(payload)))
	b = append(b, ext[:]...)
	b = append(b, payload...)
	boxes := collect(t, readerOf(t, b))
	if len(boxes) != 1 || boxes[0].Size != 20 || boxes[0].HdrSize != 16 {
		t.Fatalf("extended size parse failed: %+v", boxes)
	}
}

func TestZeroSizeExtendsToEnd(t *testing.T) {
	data := append(mkbox("ftyp", []byte("isom")), be32(0)...)
	data = append(data, "mdat"...)
	data = append(data, []byte("payload-bytes")...)
	boxes := collect(t, readerOf(t, data))
	if len(boxes) != 2 {
		t.Fatalf("want 2 boxes, got %+v", boxes)
	}
	if boxes[1].Type != "mdat" || boxes[1].End() != int64(len(data)) {
		t.Fatalf("zero-size box did not extend to EOF: %+v", boxes[1])
	}
}

func TestUUIDSkipped(t *testing.T) {
	payload := append(make([]byte, 16), []byte("user")...) // usertype + data
	data := mkbox("uuid", payload)
	boxes := collect(t, readerOf(t, data))
	if len(boxes) != 1 || boxes[0].HdrSize != 24 || boxes[0].DataSize() != 4 {
		t.Fatalf("uuid header wrong: %+v", boxes)
	}
}

func TestTruncatedBox(t *testing.T) {
	data := append(be32(1000), "mdat"...)
	data = append(data, []byte("short")...)
	_, err := collectE(readerOf(t, data))
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("want ErrTruncated, got %v", err)
	}
}

func collectE(r *Reader) ([]Box, error) {
	var boxes []Box
	err := r.WalkTop(func(b Box) (bool, error) {
		boxes = append(boxes, b)
		return false, nil
	})
	return boxes, err
}

func TestDepthLimit(t *testing.T) {
	// Nest moov-like containers deeper than MaxDepth.
	data := []byte{}
	closers := 0
	for i := 0; i < MaxDepth+3; i++ {
		data = append(data, be32(0)...) // placeholder, fixed below
		data = append(data, "moov"...)
		closers++
	}
	// Fix sizes so each moov extends to EOF (zero-size), which is legal
	// only for the last box of a region; instead compute real sizes.
	_ = closers
	// Rebuild with explicit sizes.
	data = nil
	total := (MaxDepth + 3) * 8
	for i := 0; i < MaxDepth+3; i++ {
		sz := uint32(total - i*8)
		data = append(data, be32(sz)...)
		data = append(data, "moov"...)
	}
	err := readerOf(t, data).WalkTop(func(b Box) (bool, error) { return true, nil })
	if !errors.Is(err, ErrTooDeep) {
		t.Fatalf("want ErrTooDeep, got %v", err)
	}
}

func TestNestedChildren(t *testing.T) {
	inner := append(mkbox("aabb", []byte("xx")), mkbox("ccdd", nil)...)
	data := mkbox("moov", inner)
	r := readerOf(t, data)
	var kids []Box
	err := r.WalkTop(func(b Box) (bool, error) {
		c, err := r.Children(b)
		if err != nil {
			return false, err
		}
		kids = c
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(kids) != 2 || kids[0].Type != "aabb" || kids[1].Path != "moov/ccdd" {
		t.Fatalf("children wrong: %+v", kids)
	}
}

func TestReadBoxDataCap(t *testing.T) {
	data := mkbox("free", make([]byte, 32))
	r := readerOf(t, data)
	boxes := collect(t, r)
	if _, err := r.ReadBoxData(boxes[0], 16); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("want cap error, got %v", err)
	}
	got, err := r.ReadBoxData(boxes[0], 64)
	if err != nil || len(got) != 32 {
		t.Fatalf("read failed: %v %d", err, len(got))
	}
}
