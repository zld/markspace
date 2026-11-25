package render

import (
	"os"
	"path/filepath"
	"testing"

	"medpipe/internal/clock"
	"medpipe/internal/ingest"
	"medpipe/internal/timeline"
)

func renderFrame(track uint16, pts int64, offset int64) timeline.Frame {
	w, h := 2, 2
	pixels := make([]byte, w*h*3)
	for i := range pixels {
		pixels[i] = byte((int(track)*31 + int(pts) + i) & 0xff)
	}
	f := &ingest.Frame{
		TrackID: track, PTS: pts, Timebase: clock.RTP90K, Width: w, Height: h,
		PixelFormat: ingest.PixelRGB24, Pixels: pixels, Offset: offset,
	}
	return timeline.Frame{Frame: f, TimestampNS: clock.MustNanos(pts, clock.RTP90K)}
}

func TestEncodePPMHeaderAndPixels(t *testing.T) {
	tf := renderFrame(1, 0, 0)
	b, err := EncodePPM(tf.Frame)
	if err != nil {
		t.Fatal(err)
	}
	wantHeader := "P6\n2 2\n255\n"
	if string(b[:len(wantHeader)]) != wantHeader {
		t.Fatalf("header=%q", b[:len(wantHeader)])
	}
	if len(b) != len(wantHeader)+12 {
		t.Fatalf("ppm length=%d", len(b))
	}
}

func TestExportDeterministicFilesAndHash(t *testing.T) {
	frames := []timeline.Frame{renderFrame(1, 0, 0), renderFrame(2, 90000, 72)}
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	m1, err := Export(frames, dir1)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := Export(frames, dir2)
	if err != nil {
		t.Fatal(err)
	}
	if m1.SegmentSHA256 != m2.SegmentSHA256 || m1.SegmentSHA256Hex() == "" {
		t.Fatalf("hashes differ: %s vs %s", m1.SegmentSHA256Hex(), m2.SegmentSHA256Hex())
	}
	if len(m1.SegmentSHA256Hex()) != 64 {
		t.Fatalf("hash length %d", len(m1.SegmentSHA256Hex()))
	}
	path := filepath.Join(dir1, "segment_00001.ppm")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("export missing: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := EncodePPM(frames[0].Frame)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("exported PPM does not match encoder")
	}
}

func TestExportEmptyTimeline(t *testing.T) {
	m, err := Export(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if m.FrameCount != 0 || len(m.Segments) != 0 || m.SegmentSHA256Hex() == "" {
		t.Fatalf("empty manifest should still have deterministic chain hash: %+v", m)
	}
}
