package ebml

import (
	"testing"

	"tracecheck/internal/serr"
)

func TestVINTWidth(t *testing.T) {
	cases := []struct {
		first byte
		width int
		ok    bool
	}{
		{0x80, 1, true},   // 10000000
		{0xFF, 1, true},   // unknown length, width 1
		{0x40, 2, true},   // 01000000
		{0x20, 3, true},   // 00100000
		{0x10, 4, true},   // 00010000
		{0x08, 5, true},   // 00001000
		{0x04, 6, true},   // 00000100
		{0x02, 7, true},   // 00000010
		{0x01, 8, true},   // 00000001
		{0x00, 0, false},  // would need 9 bytes
	}
	for _, c := range cases {
		w, ok := VINTWidth(c.first)
		if w != c.width || ok != c.ok {
			t.Errorf("VINTWidth(0x%02x) = (%d,%v), want (%d,%v)", c.first, w, ok, c.width, c.ok)
		}
	}
}

func TestDecodeVINT(t *testing.T) {
	cases := []struct {
		name    string
		buf     []byte
		value   uint64
		width   int
		unknown bool
	}{
		{"zero width1", []byte{0x80}, 0, 1, false},
		{"one width1", []byte{0x81}, 1, 1, false},
		{"max width1", []byte{0xFE}, 126, 1, false},
		{"unknown width1", []byte{0xFF}, 127, 1, true},
		{"unknown width4", []byte{0x1F, 0xFF, 0xFF, 0xFF}, 0x0FFFFFFF, 4, true},
		{"ebml header id", []byte{0x1A, 0x45, 0xDF, 0xA3}, 0x1A45DFA3, 4, false},
		{"width2", []byte{0x42, 0x05}, 0x0205, 2, false},
		{"width8", []byte{0x01, 0x89, 0x70, 0x8D, 0x54, 0x30, 0x80, 0x00},
			0x0189708D54308000, 8, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, w, u, err := DecodeVINT(c.buf)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if v != c.value || w != c.width || u != c.unknown {
				t.Errorf("DecodeVINT = (%d,%d,%v), want (%d,%d,%v)", v, w, u, c.value, c.width, c.unknown)
			}
		})
	}
}

func TestDecodeVINTErrors(t *testing.T) {
	if _, _, _, err := DecodeVINT(nil); err == nil || err.Code != serr.CodeTruncatedHeader {
		t.Fatalf("empty buffer: got %v", err)
	}
	// First byte 0x00 implies a 9-byte VINT.
	if _, _, _, err := DecodeVINT([]byte{0x00, 0x01}); err == nil || err.Code != serr.CodeIllegalVINT {
		t.Fatalf("over-wide VINT: got %v", err)
	}
	// Declared width 4 but only two bytes available.
	if _, _, _, err := DecodeVINT([]byte{0x1A, 0x45}); err == nil || err.Code != serr.CodeTruncatedHeader {
		t.Fatalf("truncated VINT: got %v", err)
	}
}

func TestVINTRoundTrip(t *testing.T) {
	values := []uint64{0, 1, 126, 127, 16382, 16383, 0x0FFFFFFE, 0x0FFFFFFF,
		1 << 40, 1<<56 - 2}
	for _, val := range values {
		enc, err := EncodeVINT(val)
		if err != nil {
			t.Fatalf("encode %d: %v", val, err)
		}
		got, w, unknown, derr := DecodeVINT(enc)
		if derr != nil {
			t.Fatalf("decode %x: %v", enc, derr)
		}
		if got != val || w != len(enc) || unknown {
			t.Errorf("roundtrip %d: got (%d,w=%d,u=%v)", val, got, w, unknown)
		}
	}
}

func TestEncodeUnknownVINT(t *testing.T) {
	for w := 1; w <= 8; w++ {
		b, err := EncodeUnknownVINT(w)
		if err != nil {
			t.Fatalf("width %d: %v", w, err)
		}
		if len(b) != w {
			t.Fatalf("width %d: got %d bytes", w, len(b))
		}
		v, gotW, unknown, err := DecodeVINT(b)
		if err != nil {
			t.Fatalf("decode unknown width %d: %v", w, err)
		}
		if !unknown || gotW != w {
			t.Errorf("width %d: decoded value=%d unknown=%v", w, v, unknown)
		}
	}
	if _, err := EncodeUnknownVINT(9); err == nil {
		t.Error("width 9 must be rejected")
	}
}
