package render

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"mediaforensics/internal/fixture"
	"mediaforensics/internal/model"
	"mediaforensics/internal/parse"
	"mediaforensics/internal/repair"
)

func renderCfg(dir string, frames int) Config {
	return Config{
		OutputDir:       dir,
		FrameCount:      frames,
		FrameRateNum:    24000,
		FrameRateDen:    1001,
		TransitionEvery: 8,
	}
}

func TestRenderDeterministicFramesAndHashes(t *testing.T) {
	data, _, err := fixture.GenerateMP4(fixture.DefaultSpec(42, 40))
	if err != nil {
		t.Fatal(err)
	}
	res, err := parse.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	repair.Segment(res.Timeline)

	dir1, dir2 := t.TempDir()+"/a", t.TempDir()+"/b"
	r1, e1 := NewRenderer(res.Timeline, renderCfg(dir1, 30))
	r2, e2 := NewRenderer(res.Timeline, renderCfg(dir2, 30))
	if len(e1) != 0 || len(e2) != 0 {
		t.Fatalf("renderer setup errors: %v %v", e1, e2)
	}
	m1, err := r1.Render(30)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := r2.Render(30)
	if err != nil {
		t.Fatal(err)
	}
	if len(m1.Frames) != 30 || m1.GapCount != 0 {
		t.Fatalf("intact render must have no gaps: %+v", m1.GapCount)
	}
	for i := range m1.Frames {
		if m1.Frames[i].SHA256 != m2.Frames[i].SHA256 {
			t.Fatalf("frame %d hash differs between runs", i)
		}
		// Each declared hash must match the bytes on disk.
		raw, err := os.ReadFile(filepath.Join(dir1, frameName(i)))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != m1.Frames[i].SHA256 {
			t.Fatalf("frame %d on-disk hash mismatch", i)
		}
	}
}

func TestTransitionBlendRecordsBothSources(t *testing.T) {
	data, _, err := fixture.GenerateMP4(fixture.DefaultSpec(42, 40))
	if err != nil {
		t.Fatal(err)
	}
	res, _ := parse.Parse(data)
	repair.Segment(res.Timeline)
	dir := t.TempDir()
	r, errs := NewRenderer(res.Timeline, renderCfg(dir, 32))
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	manifest, err := r.Render(32)
	if err != nil {
		t.Fatal(err)
	}
	var transitions int
	for _, f := range manifest.Frames {
		if f.Transition {
			transitions++
			if f.PrimaryTrack == 0 || f.SecondaryTrack == 0 {
				t.Fatalf("transition missing source tracks: %+v", f)
			}
			if f.PrimaryOffset < 0 || f.SecondaryOffset < 0 {
				t.Fatalf("transition must carry source offsets: %+v", f)
			}
			if f.BlendWeightA <= 0 || f.BlendWeightA > 256 {
				t.Fatalf("blend weight out of range: %d", f.BlendWeightA)
			}
		}
	}
	if transitions == 0 {
		t.Fatal("expected transition frames in the sequence")
	}
}

// Cross-module path: an index rebuild defect (missing stco/stsc plus a
// corrupted region) must surface as explicit gap frames in the render output.
func TestIndexDefectPropagatesToFrameGap(t *testing.T) {
	spec := fixture.DefaultSpec(42, 40)
	mspec, _ := fixture.MissingIndex(spec, 1)
	data, layout, err := fixture.GenerateMP4(mspec)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the first primary-track sample so the boundary-rebuilt track
	// has no usable sample at the earliest frame time.
	bad, _, err := fixture.CorruptSample(data, layout, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	res, err := parse.Parse(bad)
	if err != nil {
		t.Fatal(err)
	}
	rep := repair.Segment(res.Timeline)
	if rep.Excluded == 0 {
		t.Fatal("repair stage must exclude the damaged sample")
	}
	dir := t.TempDir()
	r, _ := NewRenderer(res.Timeline, renderCfg(dir, 12))
	manifest, err := r.Render(12)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.GapCount == 0 {
		t.Fatal("index/corruption defect must produce at least one gap frame")
	}
	var sawGap bool
	for _, f := range manifest.Frames {
		if f.Gap {
			sawGap = true
			if f.PrimaryOffset < 0 && f.PrimaryTrack != 1 {
				t.Fatalf("gap frame must identify the affected track: %+v", f)
			}
		}
	}
	if !sawGap {
		t.Fatal("gap provenance not recorded")
	}
	// The gap frame's sentinel PPM still exists and is hashable.
	first := manifest.Frames[0]
	if first.SHA256 == "" {
		t.Fatal("gap frames must still carry a checksum")
	}
}

func TestFrameCapRejected(t *testing.T) {
	tl := &model.Timeline{}
	if _, errs := NewRenderer(tl, Config{FrameCount: MaxFrames + 1}); len(errs) == 0 {
		t.Fatal("renderer must reject frame counts above the 10000 cap")
	}
}

func TestPPMHeaderAndSize(t *testing.T) {
	dir := t.TempDir()
	pixels := rasterFor(model.Sample{TrackID: 1, Index: 0}, 0)
	if err := writePPM(filepath.Join(dir, "x.ppm"), pixels); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "x.ppm"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw[:3]) != "P6\n" {
		t.Fatal("PPM magic missing")
	}
	want := len(raw) - (len("P6\n64 48\n255\n"))
	if want != FrameWidth*FrameHeight*3 {
		t.Fatalf("raster byte count = %d", want)
	}
}
