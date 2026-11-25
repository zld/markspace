package ingest

import (
	"bytes"
	"crypto/sha256"
	"strings"
	"testing"

	"medpipe/internal/clock"
)

func testFrame(t *testing.T, track uint16, pts int64, w, h int, key bool) *Frame {
	t.Helper()
	pixels := make([]byte, w*h*3)
	for i := range pixels {
		pixels[i] = byte((int(track)*17 + int(pts)*7 + i*3) & 0xff)
	}
	return &Frame{
		TrackID: track, PTS: pts, Timebase: clock.RTP90K,
		Width: w, Height: h, KeyFrame: key, PixelFormat: PixelRGB24,
		Color:  ColorMetadata{Primaries: 1, Transfer: 13, Matrix: 1, FullRange: true},
		Pixels: pixels,
	}
}

func mustEncode(t *testing.T, f *Frame) []byte {
	t.Helper()
	b, err := EncodeFrame(f)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEncodeParseRoundTrip(t *testing.T) {
	f := testFrame(t, 7, 9000, 4, 3, true)
	b := mustEncode(t, f)
	res, err := ParseBytes(b, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Frames) != 1 || len(res.Failures) != 0 {
		t.Fatalf("frames=%d failures=%+v", len(res.Frames), res.Failures)
	}
	got := res.Frames[0]
	if got.Offset != 0 || got.TrackID != 7 || got.PTS != 9000 ||
		got.Width != 4 || got.Height != 3 || !got.KeyFrame ||
		got.Color != f.Color || got.SHA256 != sha256.Sum256(f.Pixels) {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if !bytes.Equal(got.Pixels, f.Pixels) {
		t.Fatal("pixels differ")
	}
}

func TestChecksumMismatchOffsetAndRecovery(t *testing.T) {
	bad := testFrame(t, 2, 100, 2, 2, true)
	good := testFrame(t, 3, 200, 2, 2, false)
	badBytes := mustEncode(t, bad)
	goodBytes := mustEncode(t, good)
	recordEnd := len(badBytes)
	badBytes[recordEnd-1] ^= 0xff // final pixel byte; checksum now mismatches

	var stream []byte
	stream = append(stream, badBytes...)
	stream = append(stream, goodBytes...)
	res, err := ParseBytes(stream, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Frames) != 1 || res.Frames[0].Offset != int64(recordEnd) {
		t.Fatalf("expected recovered frame at offset %d, got %+v", recordEnd, res.Frames)
	}
	if len(res.Failures) != 1 {
		t.Fatalf("failures=%+v", res.Failures)
	}
	f := res.Failures[0]
	if f.Code != "checksum_mismatch" || f.Offset != 0 || f.TrackID != 2 || !f.Recovered {
		t.Fatalf("bad checksum failure: %+v", f)
	}
	if res.Recovered != 1 {
		t.Fatalf("recovered=%d want 1", res.Recovered)
	}
}

func TestInvalidDimensionsReportOffset(t *testing.T) {
	b := mustEncode(t, testFrame(t, 4, 1, 2, 2, true))
	b[25] = 0 // width high byte was zero; low byte at 25 changes 2 -> 0
	res, err := ParseBytes(b, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Frames) != 0 || len(res.Failures) != 1 {
		t.Fatalf("frames=%d failures=%+v", len(res.Frames), res.Failures)
	}
	f := res.Failures[0]
	if f.Code != "invalid_dimensions" || f.Offset != 0 || f.TrackID != 4 {
		t.Fatalf("unexpected failure %+v", f)
	}
}

func TestTruncatedBoundariesDoNotReadOutOfBounds(t *testing.T) {
	whole := mustEncode(t, testFrame(t, 9, 1, 3, 2, true))
	good := mustEncode(t, testFrame(t, 1, 0, 2, 1, true))

	for _, cut := range []int{1, FrameHeaderSize - 1, FrameHeaderSize, len(whole) - 1} {
		t.Run(string(rune(cut)), func(t *testing.T) {
			res, err := ParseBytes(whole[:cut], Options{})
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Frames) != 0 || len(res.Failures) != 1 {
				t.Fatalf("cut=%d frames=%d failures=%+v", cut, len(res.Frames), res.Failures)
			}
			if res.Failures[0].Offset != 0 || !strings.HasPrefix(res.Failures[0].Code, "truncated_") {
				t.Fatalf("cut=%d bad failure %+v", cut, res.Failures[0])
			}
		})
	}

	stream := append(append([]byte{}, good...), whole[:len(whole)-1]...)
	res, err := ParseBytes(stream, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Frames) != 1 || res.Frames[0].Offset != 0 {
		t.Fatalf("expected preceding good frame, got %+v", res.Frames)
	}
	if len(res.Failures) != 1 || res.Failures[0].Offset != int64(len(good)) {
		t.Fatalf("truncated offset failures=%+v goodLen=%d", res.Failures, len(good))
	}
}

func TestMaxFrameBytesRejectsLengthWithoutLargeAllocation(t *testing.T) {
	b := mustEncode(t, testFrame(t, 1, 1, 4, 4, true))
	res, err := ParseBytes(b, Options{MaxFrameBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Frames) != 0 || len(res.Failures) != 1 {
		t.Fatalf("frames=%d failures=%+v", len(res.Frames), res.Failures)
	}
	if res.Failures[0].Code != "frame_too_large" {
		t.Fatalf("failure=%+v", res.Failures[0])
	}
}
