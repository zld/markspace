package rtpcheck

import (
	"bytes"
	"errors"
	"testing"
)

func vp9Keyframe(n int) []byte {
	// Minimal VP9 keyframe header followed by n bytes of payload.
	hdr := []byte{0x82, 0x49, 0x83, 0x42, 0x00, 0x10, 0x00, 0x10, 0x00, 0x00, 0x00}
	out := append([]byte{}, hdr...)
	for i := 0; i < n; i++ {
		out = append(out, byte(i%251))
	}
	return out
}

func TestFrameVP9Roundtrip(t *testing.T) {
	payload := vp9Keyframe(4000)
	res, err := Frame(CodecVP9, 1200, payload)
	if err != nil {
		t.Fatalf("Frame: %v", err)
	}
	if !res.Roundtrip {
		t.Fatalf("roundtrip failed: %+v", res)
	}
	if res.Packets < 2 {
		t.Fatalf("expected fragmentation, got %d packets", res.Packets)
	}
	if res.Bytes != len(payload) {
		t.Fatalf("bytes = %d, want %d", res.Bytes, len(payload))
	}
}

func TestFrameOpusRoundtrip(t *testing.T) {
	payload := bytes.Repeat([]byte{0xA5}, 320)
	res, err := Frame(CodecOpus, 1200, payload)
	if err != nil {
		t.Fatalf("Frame: %v", err)
	}
	if !res.Roundtrip || res.Packets != 1 {
		t.Fatalf("opus result = %+v", res)
	}
}

func TestFrameRejectsUnsupportedCodec(t *testing.T) {
	_, err := Frame(Codec("V_AV1"), 1200, []byte{1, 2, 3})
	if !errors.Is(err, ErrUnsupportedCodec) {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckSegmentPerPayloadIsolation(t *testing.T) {
	// Two payloads; each must roundtrip independently, proving no RTP packet
	// mixes bytes from two payloads (i.e. across a segment boundary).
	payloads := [][]byte{vp9Keyframe(3000), vp9Keyframe(2500)}
	results, err := CheckSegment(CodecVP9, 900, payloads)
	if err != nil {
		t.Fatalf("CheckSegment: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d", len(results))
	}
	for i, r := range results {
		if !r.Roundtrip || r.Bytes != len(payloads[i]) {
			t.Fatalf("payload %d: %+v", i, r)
		}
	}
}

func TestCheckSegmentRejectsBadPayload(t *testing.T) {
	// A truncated VP9 keyframe header cannot be framed.
	_, err := CheckSegment(CodecVP9, 1200, [][]byte{{0x82}})
	if err == nil {
		t.Fatal("expected error for malformed payload")
	}
}
