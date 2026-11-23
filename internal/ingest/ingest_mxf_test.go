package ingest

import (
	"os"
	"path/filepath"
	"testing"

	"mediapipe/internal/fault"
	"mediapipe/internal/model"
	"mediapipe/internal/testdata"
)

func writeFile(path string, data []byte) error { return os.WriteFile(path, data, 0o644) }

func writeMXFFixture(t *testing.T, dir string, unknown bool) string {
	t.Helper()
	path := filepath.Join(dir, "clip.mxf")
	err := testdata.WriteMXF(path, testdata.MXFSpec{
		Seed: 42, Timescale: 30000, Rotation: 180, FrameCount: 30,
		FrameSize: 128, KeyEvery: 10, UnknownKLV: unknown,
		Colors: []testdata.ColorSpec{{Primaries: 9, Transfer: 16, Matrix: 9, FullRange: true}, {Primaries: 1, Transfer: 1, Matrix: 1, FullRange: false}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseMXFHappyPath(t *testing.T) {
	dir := t.TempDir()
	path := writeMXFFixture(t, dir, true)
	var errs fault.Batch
	tracks, err := ParseMXF(path, &errs)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 {
		t.Fatalf("want 1 track, got %d", len(tracks))
	}
	tr := tracks[0]
	if tr.Container != model.ContainerMXF {
		t.Fatalf("container = %s", tr.Container)
	}
	if tr.Rotation.Degrees != 180 || tr.Rotation.SourceOffset < 0 {
		t.Errorf("rotation not traced: %+v", tr.Rotation)
	}
	if len(tr.Color.Declarations) != 2 {
		t.Errorf("color declarations = %d", len(tr.Color.Declarations))
	}
	if len(tr.Samples) != 30 {
		t.Fatalf("samples = %d, want 30", len(tr.Samples))
	}
	keys := 0
	for _, s := range tr.Samples {
		if s.DurationNanos < 0 {
			t.Fatal("negative duration")
		}
		if s.Keyframe {
			keys++
		}
		if s.PayloadOff < 0 {
			t.Fatal("essence offset not mapped")
		}
	}
	if keys != 3 {
		t.Errorf("keyframes = %d, want 3", keys)
	}
	if !errs.HasKind(fault.KindUnknown) {
		t.Error("unknown KLV must be reported with offset")
	}
	for _, e := range errs.All() {
		if e.Kind == fault.KindUnknown && e.Offset < 0 {
			t.Error("unknown structure missing byte offset")
		}
	}
}

func TestParseMXFTruncated(t *testing.T) {
	dir := t.TempDir()
	path := writeMXFFixture(t, dir, false)
	dst := filepath.Join(dir, "trunc.mxf")
	off, err := testdata.TruncateKLV(path, dst)
	if err != nil {
		t.Fatal(err)
	}
	var errs fault.Batch
	tracks, _ := ParseMXF(dst, &errs)
	if !errs.HasKind(fault.KindTruncated) {
		t.Fatalf("want truncation fault, got %v", errs.All())
	}
	for _, e := range errs.All() {
		if e.Kind == fault.KindTruncated {
			if e.Offset != off {
				t.Errorf("offset=%d want %d", e.Offset, off)
			}
			if !e.Recoverable {
				t.Error("must be recoverable")
			}
		}
	}
	// Frames whose index records survived remain usable.
	if len(tracks) == 0 || len(tracks[0].Samples) == 0 {
		t.Fatal("surviving frames should still be produced")
	}
}

func TestParseMXFRejectsBadPrefix(t *testing.T) {
	dir := t.TempDir()
	path := writeMXFFixture(t, dir, false)
	data, _ := readInput(path)
	data[0] = 0x01 // break SMPTE UL prefix
	dst := filepath.Join(dir, "bad.mxf")
	if err := writeFile(dst, data); err != nil {
		t.Fatal(err)
	}
	var errs fault.Batch
	if _, err := ParseMXF(dst, &errs); err == nil {
		t.Fatal("bad UL prefix must be a non-recoverable input error")
	}
	if errs.Fatal() == nil {
		t.Fatal("expected a fatal fault")
	}
}
