package scan

import (
	"testing"

	"mediarc/internal/model"
)

// obu builds one OBU with a size field: header byte, leb128 size, payload.
func obu(typ byte, payload ...byte) []byte {
	hdr := typ<<3 | 0x02 // has_size_field
	out := []byte{hdr, byte(len(payload))}
	return append(out, payload...)
}

func TestScanAV1(t *testing.T) {
	var data []byte
	data = append(data, obu(2)...)             // temporal delimiter
	data = append(data, obu(6, 0x00, 0xAA)...) // frame, key (frame_type=0)
	data = append(data, obu(2)...)
	data = append(data, obu(6, 0x20, 0xBB)...) // frame, inter (frame_type=1)
	aus, err := ScanAV1(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(aus) != 2 {
		t.Fatalf("got %d AUs, want 2", len(aus))
	}
	if !aus[0].Keyframe || aus[1].Keyframe {
		t.Fatalf("keyframe flags wrong: %+v", aus)
	}
	if aus[0].Offset != 0 || aus[0].Length != 6 {
		t.Fatalf("au0 = %+v, want offset 0 length 6", aus[0])
	}
	if aus[1].Offset != 6 || aus[1].Length != 6 {
		t.Fatalf("au1 = %+v, want offset 6 length 6", aus[1])
	}
}

func TestScanAV1Truncated(t *testing.T) {
	data := append(obu(2), 0x32, 0x05, 0x00) // frame OBU declares 5, has 1
	aus, err := ScanAV1(data)
	if err == nil || err.Code != model.ErrTruncated {
		t.Fatalf("want TRUNCATED, got %v", err)
	}
	if err.Offset != 2 {
		t.Fatalf("error offset = %d, want 2", err.Offset)
	}
	if len(aus) != 1 {
		t.Fatalf("got %d AUs, want 1", len(aus))
	}
}

func TestScanAV1ForbiddenBit(t *testing.T) {
	if _, err := ScanAV1([]byte{0x80}); err == nil || err.Code != model.ErrMisaligned {
		t.Fatalf("want MISALIGNED, got %v", err)
	}
}

func TestLeb128(t *testing.T) {
	if v, n := leb128([]byte{0xE5, 0x8E, 0x26}); v != 624485 || n != 3 {
		t.Fatalf("leb128 = %d,%d want 624485,3", v, n)
	}
	if _, n := leb128([]byte{0x80}); n != 0 {
		t.Fatal("truncated leb128 should consume 0")
	}
}
