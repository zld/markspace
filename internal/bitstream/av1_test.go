package bitstream

import (
	"bytes"
	"testing"

	"mediacore/internal/fault"
)

// buildOBU assembles a low-overhead AV1 OBU with an explicit size field.
func buildOBU(typ int, payload []byte) []byte {
	out := []byte{byte(typ<<3) | 0x02}
	n := len(payload)
	for {
		b := byte(n & 0x7F)
		n >>= 7
		if n > 0 {
			out = append(out, b|0x80)
		} else {
			out = append(out, b)
			break
		}
	}
	return append(out, payload...)
}

func av1SeqHeader() []byte       { return []byte{0x00, 0x00, 0x00} }
func av1KeyFramePayload() []byte { return []byte{0x00, 0x00, 0x00, 0x00} }
func av1InterPayload() []byte    { return []byte{0x20, 0x00, 0x00, 0x00} }
func av1ShowExisting() []byte    { return []byte{0x80, 0x00} }

func TestScanAV1Keyframes(t *testing.T) {
	var b bytes.Buffer
	b.Write(buildOBU(obuTemporalDelim, nil))
	b.Write(buildOBU(obuSequenceHeader, av1SeqHeader()))
	b.Write(buildOBU(obuFrame, av1KeyFramePayload()))
	for i := 0; i < 3; i++ {
		b.Write(buildOBU(obuTemporalDelim, nil))
		b.Write(buildOBU(obuFrame, av1InterPayload()))
	}
	// Frame OBUs carry their own header, so a keyframe stays key even after a
	// standalone header OBU.
	b.Write(buildOBU(obuTemporalDelim, nil))
	b.Write(buildOBU(obuFrameHeader, av1KeyFramePayload()))
	b.Write(buildOBU(obuTemporalDelim, nil))
	b.Write(buildOBU(obuFrame, av1KeyFramePayload()))
	// A display-only header must not become a picture.
	b.Write(buildOBU(obuTemporalDelim, nil))
	b.Write(buildOBU(obuFrameHeader, av1ShowExisting()))
	b.Write(buildOBU(obuTemporalDelim, nil))
	b.Write(buildOBU(obuFrame, av1InterPayload()))

	s, err := Scan(CodecAV1, b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Frames) != 6 {
		t.Fatalf("frames = %d want 6", len(s.Frames))
	}
	if keys := s.KeyframeOffsets(); len(keys) != 2 {
		t.Fatalf("keyframes = %v", keys)
	}
	for i := 1; i < len(s.Frames); i++ {
		if s.Frames[i].Offset <= s.Frames[i-1].Offset {
			t.Fatalf("offsets not increasing: %v", s.Frames)
		}
	}
}

func TestScanAV1MissingSequenceHeader(t *testing.T) {
	_, err := Scan(CodecAV1, buildOBU(obuFrame, av1KeyFramePayload()))
	f, ok := fault.As(err)
	if !ok || f.Code != fault.CodeScanMissingParameter {
		t.Fatalf("err = %v", err)
	}
}

func TestScanAV1TruncatedOBU(t *testing.T) {
	data := buildOBU(obuSequenceHeader, av1SeqHeader())
	// Claim a payload of 1000 bytes while only a few follow.
	data = append(data, []byte{byte(obuFrame<<3) | 0x02, 0xE8, 0x07, 0x00, 0x00}...)
	_, err := Scan(CodecAV1, data)
	f, ok := fault.As(err)
	if !ok || f.Code != fault.CodeScanTruncatedHeader {
		t.Fatalf("err = %v", err)
	}
}

func TestScanAV1ReservedBit(t *testing.T) {
	data := []byte{byte(obuSequenceHeader<<3) | 0x03, 0x00, 0x00, 0x00}
	_, err := Scan(CodecAV1, data)
	f, ok := fault.As(err)
	if !ok || f.Code != fault.CodeScanIllegalBoundary || !f.HasOffset {
		t.Fatalf("err = %v", err)
	}
}
