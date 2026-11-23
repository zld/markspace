package codec

import (
	"testing"

	"mediaroute/internal/clock"
)

func annexB(units ...[]byte) []byte {
	var out []byte
	for _, u := range units {
		out = append(out, 0, 0, 0, 1)
		out = append(out, u...)
	}
	return out
}

func TestSplitAnnexBAndTypes(t *testing.T) {
	sps := []byte{0x67, 0xAA} // H.264 SPS header (type 7)
	idrp := []byte{0x65, 0xBB, 0xCC} // H.264 IDR (type 5)
	nonIDR := []byte{0x41, 0xDD} // H.264 non-IDR slice (type 1)
	buf := annexB(sps, idrp, nonIDR)
	// Reassembler appends a trailing start code to mark the frame complete.
	buf = append(buf, 0, 0, 1)
	units := SplitAnnexB(buf)
	if len(units) != 3 {
		t.Fatalf("unit count = %d", len(units))
	}
	for i, u := range units {
		if !u.Complete || u.StartLen == 0 || u.Offset < 0 {
			t.Fatalf("unit %d not complete: %+v", i, u)
		}
	}
	if H264NALType(units[0].Header) != 7 || H264NALType(units[1].Header) != 5 {
		t.Fatal("NAL type extraction wrong")
	}
	got, err := FrameKey(H264, buf)
	if err != nil || !got {
		t.Fatalf("H.264 keyframe not detected: %v %v", got, err)
	}
}

func TestTruncatedNALNoHit(t *testing.T) {
	idr := []byte{0x65, 0x11, 0x22, 0x33}
	truncated := append([]byte{0, 0, 1}, idr...) // no trailing start code
	units := SplitAnnexB(truncated)
	if len(units) != 1 || units[0].Complete {
		t.Fatalf("truncated unit misclassified: %+v", units)
	}
	got, err := FrameKey(H264, truncated)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("truncated IDR must not produce a keyframe hit")
	}
}

func TestH265KeyframeTypes(t *testing.T) {
	// H.265 IDR_W_RADL type 19 -> header byte (19<<1)=0x26.
	idr := []byte{byte(19 << 1), 0x01}
	buf := append(annexB(idr), 0, 0, 1)
	if H265NALType(buf[4]) != 19 {
		t.Fatal("H.265 type extraction wrong")
	}
	got, err := FrameKey(H265, buf)
	if err != nil || !got {
		t.Fatalf("H.265 IDR not detected: %v %v", got, err)
	}
}

func TestUnsupportedCodec(t *testing.T) {
	_, err := FrameKey(Codec("vc-1"), []byte{0, 0, 1, 0x65})
	if err == nil {
		t.Fatal("unknown codec accepted")
	}
	pe, ok := clock.AsError(err)
	if !ok || pe.Recoverable || pe.Phase != clock.PhaseCodec {
		t.Fatalf("unsupported codec error model wrong: %v", err)
	}
}

func TestNoStartCodes(t *testing.T) {
	if units := SplitAnnexB([]byte{1, 2, 3, 4}); units != nil {
		t.Fatalf("random bytes parsed as NAL: %+v", units)
	}
}
