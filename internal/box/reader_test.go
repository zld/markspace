package box

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func mkbox(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b, uint32(8+len(payload)))
	copy(b[4:], typ)
	copy(b[8:], payload)
	return b
}

func readerOf(data []byte) *Reader {
	return NewReader(bytes.NewReader(data), int64(len(data)))
}

func TestRead32BitBox(t *testing.T) {
	data := mkbox("free", []byte{1, 2, 3})
	rd := readerOf(data)
	b, err := rd.At(0, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if b.Type != "free" || b.Size != 11 || b.HeadSize != 8 {
		t.Fatalf("got %+v", b)
	}
}

func TestRead64BitBox(t *testing.T) {
	payload := []byte{9, 9}
	b := make([]byte, 16+len(payload))
	binary.BigEndian.PutUint32(b[0:4], 1)
	copy(b[4:8], "wide")
	binary.BigEndian.PutUint64(b[8:16], uint64(len(b)))
	copy(b[16:], payload)
	rd := readerOf(b)
	got, err := rd.At(0, int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if got.HeadSize != 16 || got.Size != int64(len(b)) || got.Type != "wide" {
		t.Fatalf("got %+v", got)
	}
}

func TestSizeZeroExtendsToLimit(t *testing.T) {
	b := make([]byte, 20)
	binary.BigEndian.PutUint32(b[0:4], 0)
	copy(b[4:8], "mdat")
	rd := readerOf(b)
	got, err := rd.At(0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got.Size != 20 {
		t.Fatalf("size=0 should extend to limit, got %d", got.Size)
	}
}

func TestTruncatedHeader(t *testing.T) {
	rd := readerOf([]byte{0, 0, 0})
	if _, err := rd.At(0, 3); err == nil || !strings.Contains(err.Error(), "offset 0") {
		t.Fatalf("want truncated-header error with offset, got %v", err)
	}
}

func TestSizeExceedsLimit(t *testing.T) {
	data := mkbox("moov", nil)
	binary.BigEndian.PutUint32(data[0:4], 1<<20)
	rd := readerOf(data)
	_, err := rd.At(0, int64(len(data)))
	if err == nil || !strings.Contains(err.Error(), `"moov"`) || !strings.Contains(err.Error(), "offset 0") {
		t.Fatalf("error must name box type and offset, got %v", err)
	}
}

func TestSizeSmallerThanHeader(t *testing.T) {
	data := mkbox("free", nil)
	binary.BigEndian.PutUint32(data[0:4], 4)
	rd := readerOf(data)
	if _, err := rd.At(0, int64(len(data))); err == nil {
		t.Fatal("want error for size < header")
	}
}

func TestNonPrintableType(t *testing.T) {
	data := mkbox("free", nil)
	data[4] = 0x01
	rd := readerOf(data)
	if _, err := rd.At(0, int64(len(data))); err == nil {
		t.Fatal("want error for non-printable type")
	}
}

func TestChildrenOfNonContainer(t *testing.T) {
	data := mkbox("free", []byte{1, 2, 3, 4})
	rd := readerOf(data)
	b, _ := rd.At(0, int64(len(data)))
	if _, err := rd.Children(b); err == nil {
		t.Fatal("want error nesting under non-container")
	}
}

func TestChildOverrun(t *testing.T) {
	inner := mkbox("free", nil)
	binary.BigEndian.PutUint32(inner[0:4], 100) // lies about its size
	data := mkbox("moov", inner)
	rd := readerOf(data)
	top, err := rd.TopLevel()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rd.Children(top[0]); err == nil {
		t.Fatal("want error for child overrunning parent")
	}
}

func TestTopLevelWalk(t *testing.T) {
	data := append(mkbox("ftyp", []byte{1, 2, 3, 4}), mkbox("moov", nil)...)
	rd := readerOf(data)
	top, err := rd.TopLevel()
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 2 || top[0].Type != "ftyp" || top[1].Type != "moov" {
		t.Fatalf("got %+v", top)
	}
}
