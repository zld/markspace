package ebml

import (
	"bytes"
	"testing"
)

func TestParseID(t *testing.T) {
	cases := []struct {
		in    []byte
		id    uint32
		width int
		bad   bool
		trunc bool
	}{
		{[]byte{0x1A, 0x45, 0xDF, 0xA3}, 0x1A45DFA3, 4, false, false},
		{[]byte{0xA3}, 0xA3, 1, false, false},
		{[]byte{0x42, 0x82}, 0x4282, 2, false, false},
		{[]byte{0x1F, 0x43, 0xB6, 0x75}, 0x1F43B675, 4, false, false},
		{[]byte{}, 0, 0, false, true},
		{[]byte{0x00, 0x01}, 0, 0, true, false},
		{[]byte{0x08, 0x00, 0x00, 0x00, 0x00}, 0, 0, true, false}, // 5-byte ID
		{[]byte{0x18, 0x53}, 0, 0, false, true},                   // needs 4 bytes
	}
	for _, c := range cases {
		id, width, err := ParseID(c.in)
		if c.bad || c.trunc {
			if err == nil {
				t.Fatalf("ParseID(%x): expected error", c.in)
			}
			if c.bad && err != ErrInvalidVINT {
				t.Fatalf("ParseID(%x): got %v want ErrInvalidVINT", c.in, err)
			}
			if c.trunc && err != ErrTruncatedVINT {
				t.Fatalf("ParseID(%x): got %v want ErrTruncatedVINT", c.in, err)
			}
			continue
		}
		if err != nil || id != c.id || width != c.width {
			t.Fatalf("ParseID(%x) = %#x,%d,%v want %#x,%d", c.in, id, width, err, c.id, c.width)
		}
	}
}

func TestParseSizeUnknownAndWidths(t *testing.T) {
	// 0xFF is the canonical unknown size (1-byte).
	s, err := ParseSize([]byte{0xFF})
	if err != nil || !s.Unknown || s.Value != 127 || s.Width != 1 {
		t.Fatalf("unknown size = %+v, %v", s, err)
	}
	// All data bits set at width 8 is also unknown. The first byte carries the
	// 8-byte marker with all size bits set: 0x01.
	all := append([]byte{0x01}, bytes.Repeat([]byte{0xFF}, 7)...)
	s, err = ParseSize(all)
	if err != nil || !s.Unknown || s.Width != 8 {
		t.Fatalf("8-byte unknown = %+v, %v", s, err)
	}
	// Non-canonical wide sizes are still valid and roundtrip through
	// EncodeSizeWidth unchanged.
	for _, width := range []int{2, 3, 4, 8} {
		enc := EncodeSizeWidth(513, width, false)
		if len(enc) != width {
			t.Fatalf("width %d: encoded length %d", width, len(enc))
		}
		got, err := ParseSize(enc)
		if err != nil || got.Value != 513 || got.Width != width || got.Unknown {
			t.Fatalf("width %d: got %+v err %v", width, got, err)
		}
	}
	if enc := EncodeSizeWidth(127, 1, false); enc != nil {
		t.Fatalf("width 1 cannot hold 127 without colliding with unknown: %x", enc)
	}
}

func TestParseSizeErrors(t *testing.T) {
	if _, err := ParseSize(nil); err != ErrTruncatedVINT {
		t.Fatalf("nil: %v", err)
	}
	if _, err := ParseSize([]byte{0x00, 0x01}); err != ErrInvalidVINT {
		t.Fatalf("zero marker: %v", err)
	}
	if _, err := ParseSize([]byte{0x40}); err != ErrTruncatedVINT {
		t.Fatalf("short: %v", err)
	}
}

func TestVINTDeterminism(t *testing.T) {
	for v := uint64(0); v < 20000; v += 97 {
		enc := EncodeSize(v, false)
		s, err := ParseSize(enc)
		if err != nil || s.Value != v || s.Unknown {
			t.Fatalf("v=%d enc=%x got=%+v err=%v", v, enc, s, err)
		}
		if enc2 := EncodeSize(v, false); !bytes.Equal(enc, enc2) {
			t.Fatalf("nondeterministic encoding for %d", v)
		}
	}
}

func TestParseUintVINT(t *testing.T) {
	v, width, err := ParseUintVINT([]byte{0x81})
	if err != nil || v != 1 || width != 1 {
		t.Fatalf("got %d,%d,%v", v, width, err)
	}
	v, width, err = ParseUintVINT([]byte{0x40, 0x05})
	if err != nil || v != 5 || width != 2 {
		t.Fatalf("got %d,%d,%v", v, width, err)
	}
}
