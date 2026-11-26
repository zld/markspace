package scan

import (
	"testing"

	"mediarc/internal/model"
)

// h264AU builds AUD + one VCL NAL. idr selects an IDR (type 5) or non-IDR
// (type 1) VCL; payload bytes follow the NAL header.
func h264AU(idr bool, payload ...byte) []byte {
	typ := byte(0x01)
	if idr {
		typ = 0x65
	}
	out := []byte{0, 0, 0, 1, 0x09, 0x10} // AUD
	out = append(out, 0, 0, 0, 1, typ)
	return append(out, payload...)
}

var h264EOS = []byte{0, 0, 0, 1, 0x0B}

func TestScanH264(t *testing.T) {
	var data []byte
	data = append(data, h264AU(true, 0x88, 0x99)...)
	data = append(data, h264AU(false, 0x77)...)
	data = append(data, h264EOS...)
	aus, err := ScanNAL(data, CodecH264)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(aus) != 2 {
		t.Fatalf("got %d AUs, want 2", len(aus))
	}
	if !aus[0].Keyframe || aus[1].Keyframe {
		t.Fatalf("keyframe flags wrong: %+v", aus)
	}
	if aus[0].Offset != 0 || aus[0].Length != 13 {
		t.Fatalf("au0 = %+v, want offset 0 length 13", aus[0])
	}
	if aus[1].Offset != 13 || aus[1].Length != 12 {
		t.Fatalf("au1 = %+v, want offset 13 length 12", aus[1])
	}
}

func TestScanH264Truncated(t *testing.T) {
	data := append(h264AU(true, 0x88), h264AU(false, 0x77)...) // no EOS
	aus, err := ScanNAL(data, CodecH264)
	if err == nil || err.Code != model.ErrTruncated {
		t.Fatalf("want TRUNCATED, got %v", err)
	}
	if err.Offset != int64(len(data)) {
		t.Fatalf("error offset = %d, want %d", err.Offset, len(data))
	}
	if len(aus) != 1 {
		t.Fatalf("got %d complete AUs, want 1", len(aus))
	}
}

func TestScanH265(t *testing.T) {
	aud := []byte{0, 0, 0, 1, 35 << 1, 0x01}
	idr := []byte{0, 0, 0, 1, 19 << 1, 0x01, 0xAA}
	eos := []byte{0, 0, 0, 1, 36 << 1, 0x01}
	data := append(append(append([]byte{}, aud...), idr...), eos...)
	aus, err := ScanNAL(data, CodecH265)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(aus) != 1 || !aus[0].Keyframe {
		t.Fatalf("got %+v, want one keyframe AU", aus)
	}
}

func TestScanMisaligned(t *testing.T) {
	if _, err := ScanNAL([]byte{0x55, 0x66, 0x77}, CodecH264); err == nil || err.Code != model.ErrMisaligned {
		t.Fatalf("want MISALIGNED, got %v", err)
	}
}
