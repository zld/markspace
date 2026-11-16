package check_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"hlscheck/internal/check"
	"hlscheck/internal/cut"
	"hlscheck/internal/demo"
	"hlscheck/internal/hls"
)

// buildProfile slices a synthetic input into dir and writes the manifest.
func buildProfile(t *testing.T, dir string, opt demo.GenOptions) {
	t.Helper()
	var buf bytes.Buffer
	if err := demo.Generate(&buf, opt); err != nil {
		t.Fatal(err)
	}
	res, err := cut.Slice(context.Background(), &buf, dir, cut.Options{TargetDuration: 4})
	if err != nil {
		t.Fatal(err)
	}
	segs := make([]hls.Segment, len(res.Segments))
	for i, s := range res.Segments {
		segs[i] = hls.Segment{Seq: s.Seq, URI: s.Name, Duration: s.Duration, SHA256: s.SHA256}
	}
	f, err := os.Create(filepath.Join(dir, "index.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if err := hls.Encode(f, hls.New(0, segs)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func profileDir(t *testing.T, name string, seed int64) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	buildProfile(t, dir, demo.GenOptions{BitrateKbps: 450, DurationSec: 16, GOPSec: 2, Seed: seed})
	return dir
}

func TestCleanProfile(t *testing.T) {
	rep := check.CheckProfile(profileDir(t, "450", 11))
	if !rep.OK() {
		t.Fatalf("clean profile reported faults: %+v", rep)
	}
	if rep.Segments != 4 {
		t.Errorf("segments = %d, want 4", rep.Segments)
	}
	if rep.DurationDeltaMs > 100 {
		t.Errorf("duration delta = %.1f ms", rep.DurationDeltaMs)
	}
}

func TestMissingSegment(t *testing.T) {
	dir := profileDir(t, "450", 12)
	if err := os.Remove(filepath.Join(dir, "seg_00001.ts")); err != nil {
		t.Fatal(err)
	}
	rep := check.CheckProfile(dir)
	if len(rep.Missing) != 1 || rep.Missing[0] != 1 {
		t.Errorf("missing = %v, want [1]", rep.Missing)
	}
	if rep.OK() {
		t.Error("profile should not be OK")
	}
}

func TestSwappedSegments(t *testing.T) {
	dir := profileDir(t, "450", 13)
	a, b := filepath.Join(dir, "seg_00001.ts"), filepath.Join(dir, "seg_00002.ts")
	tmp := a + ".x"
	if err := os.Rename(a, tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(b, a); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, b); err != nil {
		t.Fatal(err)
	}
	rep := check.CheckProfile(dir)
	if rep.Reordered != 1 {
		t.Errorf("reordered = %d, want 1", rep.Reordered)
	}
	if len(rep.ChecksumFailures) != 2 {
		t.Errorf("checksum failures = %v, want 2 entries", rep.ChecksumFailures)
	}
}

func TestTamperedSegment(t *testing.T) {
	dir := profileDir(t, "450", 14)
	p := filepath.Join(dir, "seg_00002.ts")
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte{0xFF}, 500); err != nil {
		t.Fatal(err)
	}
	f.Close()
	rep := check.CheckProfile(dir)
	if len(rep.ChecksumFailures) != 1 || rep.ChecksumFailures[0] != 2 {
		t.Errorf("checksum failures = %v, want [2]", rep.ChecksumFailures)
	}
	if rep.Reordered != 0 {
		t.Errorf("tamper must not count as reorder")
	}
}

func TestCrossAlignment(t *testing.T) {
	root := t.TempDir()
	d1 := filepath.Join(root, "450")
	d2 := filepath.Join(root, "900")
	buildProfile(t, d1, demo.GenOptions{BitrateKbps: 450, DurationSec: 16, GOPSec: 2, Seed: 1})
	buildProfile(t, d2, demo.GenOptions{BitrateKbps: 900, DurationSec: 16, GOPSec: 2, Seed: 2})
	cr := check.CheckCross(check.CheckProfile(d1), check.CheckProfile(d2))
	if cr.BoundaryDeviationMs != 0 {
		t.Errorf("deviation = %.1f ms, want 0", cr.BoundaryDeviationMs)
	}
	if len(cr.Details) != 0 {
		t.Errorf("details = %v", cr.Details)
	}
}
