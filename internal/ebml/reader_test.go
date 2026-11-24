package ebml

import (
	"bytes"
	"io"
	"testing"

	"mediapack/internal/fault"
)

// Common Matroska element IDs used as fixtures.
const (
	idSegment uint32 = 0x18538067
	idCluster uint32 = 0x1F43B675
)

func newReader(t *testing.T, b []byte) *Reader {
	t.Helper()
	r, err := NewReader(bytes.NewReader(b), 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestReadVIntWidths(t *testing.T) {
	// 1..4 byte VINTs with data values 1, 2, 3, 4.
	// ReadVInt returns the raw VINT (marker bit retained); EBML IDs are
	// compared in that form, while size values mask the marker separately.
	cases := []struct {
		raw   []byte
		value uint64
		width int
	}{
		{[]byte{0x81}, 0x81, 1},
		{[]byte{0x40, 0x02}, 0x4002, 2},
		{[]byte{0x20, 0x00, 0x03}, 0x200003, 3},
		{[]byte{0x10, 0x00, 0x00, 0x04}, 0x10000004, 4},
	}
	var flat []byte
	for _, c := range cases {
		flat = append(flat, c.raw...)
	}
	r := newReader(t, flat)
	off := int64(0)
	for _, c := range cases {
		v, w, raw, err := r.ReadVInt(off)
		if err != nil {
			t.Fatalf("VINT at %d: %v", off, err)
		}
		if v != c.value || w != c.width || !bytes.Equal(raw, c.raw) {
			t.Fatalf("got v=%d w=%d raw=%x, want v=%d w=%d raw=%x",
				v, w, raw, c.value, c.width, c.raw)
		}
		off += int64(w)
	}
}

func TestIllegalVINTMarker(t *testing.T) {
	// 0x00 has no length marker within 8 bytes.
	r := newReader(t, make([]byte, 9))
	_, _, _, err := r.ReadVInt(0)
	if !fault.Is(err, fault.KindIllegalVINT) || fault.Of(err).Offset != 0 {
		t.Fatalf("expected illegal VINT at offset 0, got %v", err)
	}
}

func TestTruncatedVINT(t *testing.T) {
	r := newReader(t, []byte{0x10, 0x00, 0x00}) // 4-byte VINT, only 3 present
	_, _, _, err := r.ReadVInt(0)
	fe := fault.Of(err)
	if fe == nil || fe.Kind != fault.KindTruncated || !fe.Recoverable {
		t.Fatalf("expected recoverable truncation, got %v", err)
	}
}

func TestUnknownSizeElement(t *testing.T) {
	// Segment id (4 bytes) + unknown-size VINT 0xFF.
	raw := []byte{0x18, 0x53, 0x80, 0x67, 0xFF}
	r := newReader(t, raw)
	el, err := r.ReadElement()
	if err != nil {
		t.Fatal(err)
	}
	if el.ID != idSegment || el.Size != UnknownSize || el.HeaderEnd != 5 || el.End != -1 {
		t.Fatalf("unexpected element: %+v", el)
	}
}

func TestKnownElementInterval(t *testing.T) {
	// Cluster id 0x1F43B675, size 4, payload ABCD.
	raw := []byte{0x1F, 0x43, 0xB6, 0x75, 0x84, 'A', 'B', 'C', 'D'}
	r := newReader(t, raw)
	el, err := r.ReadElement()
	if err != nil {
		t.Fatal(err)
	}
	if el.ID != idCluster || el.Start != 0 || el.HeaderEnd != 5 || el.End != 9 || el.Size != 4 {
		t.Fatalf("unexpected interval: %+v", el)
	}
	payload, err := r.ReadPayload(el)
	if err != nil || string(payload) != "ABCD" {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
}

func TestLengthOutOfRange(t *testing.T) {
	// Element inside a 6-byte window declaring size 100.
	raw := []byte{0x1F, 0x43, 0xB6, 0x75, 0xE4, 0x40}
	r, err := NewReader(bytes.NewReader(raw), 0, int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.ReadElement()
	if !fault.Is(err, fault.KindLengthOutOfRange) {
		t.Fatalf("expected length-out-of-range, got %v", err)
	}
}

func TestTruncatedPayload(t *testing.T) {
	raw := []byte{0x1F, 0x43, 0xB6, 0x75, 0x84, 'A', 'B'} // size 4, 2 bytes present
	r := newReader(t, raw)
	el, err := r.ReadElement()
	// The header-stage length check already sees the declared end (9)
	// overrun the file end (7).
	if !fault.Is(err, fault.KindLengthOutOfRange) {
		t.Fatalf("expected length-out-of-range at header stage, got el=%+v err=%v", el, err)
	}

	// A truncated payload inside an explicit larger window (e.g. a cluster
	// whose own end was declared before truncation was discovered) must
	// surface a recoverable truncated fault with the failing offset.
	r2, err := NewReader(bytes.NewReader(raw), 0, 64)
	if err != nil {
		t.Fatal(err)
	}
	el2, err := r2.ReadElement()
	if err != nil {
		t.Fatal(err)
	}
	_, err = r2.ReadPayload(el2)
	fe := fault.Of(err)
	if fe == nil || fe.Kind != fault.KindTruncated || fe.Offset != 7 {
		t.Fatalf("expected truncation at byte 7, got %v", err)
	}
}

func TestChildWindowAndEOF(t *testing.T) {
	// Master element size 3 containing one 1-byte-payload child.
	// 0xEC is a legal 1-byte VINT (Matroska Void element ID).
	raw := []byte{0x18, 0x53, 0x80, 0x67, 0x83, 0xEC, 0x81, 0x07, 0x00}
	r := newReader(t, raw)
	master, err := r.ReadElement()
	if err != nil {
		t.Fatal(err)
	}
	child, err := r.Child(master)
	if err != nil {
		t.Fatal(err)
	}
	el, err := child.ReadElement()
	if err != nil || el.ID != 0xEC || el.Size != 1 {
		t.Fatalf("child read failed: el=%+v err=%v", el, err)
	}
	if err := child.Skip(el); err != nil {
		t.Fatal(err)
	}
	if _, err := child.ReadElement(); err != io.EOF {
		t.Fatalf("expected io.EOF at window end, got %v", err)
	}
}

func TestReadUintAndFloat(t *testing.T) {
	v, err := ReadUint([]byte{0x01, 0x00})
	if err != nil || v != 256 {
		t.Fatalf("uint decode wrong: %d %v", v, err)
	}
	if _, err := ReadUint(make([]byte, 9)); !fault.Is(err, fault.KindLengthOutOfRange) {
		t.Fatalf("oversized uint must fault")
	}
	if f, err := ReadFloat(nil); err != nil || f != 0 {
		t.Fatalf("empty float: %f %v", f, err)
	}
	if _, err := ReadFloat([]byte{1, 2, 3}); !fault.Is(err, fault.KindLengthOutOfRange) {
		t.Fatalf("3-byte float must fault")
	}
}
