package bitstream

import (
	"bytes"
	"testing"

	"mediacore/internal/fault"
)

// buildAnnexB writes a NAL unit with a four byte start code.
func buildAnnexB(nal []byte) []byte {
	out := []byte{0, 0, 0, 1}
	return append(out, nal...)
}

func h264SPS() []byte { return []byte{0x67, 0x42, 0x00, 0x1E, 0x88} }

func h264IDRSlice() []byte { return []byte{0x65, 0x88, 0x80} }

func h264PSlice() []byte { return []byte{0x41, 0x88, 0x80} }

func TestScanH264FramesAndKeyframes(t *testing.T) {
	var b bytes.Buffer
	b.Write(buildAnnexB(h264SPS()))
	b.Write(buildAnnexB(h264IDRSlice()))
	b.Write(buildAnnexB(h264PSlice()))
	b.Write(buildAnnexB(h264PSlice()))
	b.Write(buildAnnexB(h264SPS()))
	b.Write(buildAnnexB(h264IDRSlice()))
	s, err := Scan(CodecH264, b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Frames) != 4 {
		t.Fatalf("frames = %d want 4", len(s.Frames))
	}
	if keys := s.KeyframeOffsets(); len(keys) != 2 {
		t.Fatalf("keys = %v", keys)
	}
	if s.Frames[0].Offset != 0 {
		t.Fatalf("first frame must start at 0, got %d", s.Frames[0].Offset)
	}
	total := int64(0)
	for _, f := range s.Frames {
		total += f.Size
	}
	if total != int64(b.Len()) {
		t.Fatalf("frame sizes sum to %d want %d", total, b.Len())
	}
}

func TestScanH264IllegalLeadingBytes(t *testing.T) {
	data := append([]byte{0xDE, 0xAD}, buildAnnexB(h264SPS())...)
	_, err := Scan(CodecH264, data)
	f, ok := fault.As(err)
	if !ok || f.Code != fault.CodeScanIllegalBoundary || f.Offset != 0 {
		t.Fatalf("err = %v", err)
	}
}

func TestScanH264MissingParameterSet(t *testing.T) {
	_, err := Scan(CodecH264, buildAnnexB(h264IDRSlice()))
	f, ok := fault.As(err)
	if !ok || f.Code != fault.CodeScanMissingParameter {
		t.Fatalf("err = %v", err)
	}
}

func TestScanEmptyStream(t *testing.T) {
	_, err := Scan(CodecH264, nil)
	f, ok := fault.As(err)
	if !ok || f.Code != fault.CodeScanTruncatedHeader {
		t.Fatalf("err = %v", err)
	}
}

func TestParseCodec(t *testing.T) {
	cases := map[string]Codec{"AVC": CodecH264, "hevc": CodecH265, "av01": CodecAV1}
	for in, want := range cases {
		got, err := ParseCodec(in)
		if err != nil || got != want {
			t.Errorf("ParseCodec(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseCodec("vp9"); err == nil {
		t.Fatal("vp9 must be rejected")
	}
}
