package verify

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"medialab/internal/artifact"
	"medialab/internal/model"
	"medialab/internal/pixel"
	"medialab/internal/render"
	"medialab/internal/timeline"
)

func fixture(t *testing.T, mutate func(*artifact.Manifest)) (string, *artifact.Manifest) {
	t.Helper()
	tl := testTimeline(t)
	out := t.TempDir()
	res, err := render.Export(context.Background(), tl, render.Options{
		OutDir: out, Plan: params(), Workers: 2, CacheCap: 16 << 20,
		Decode: func(f model.Frame) (pixel.Raw, error) {
			return raw(f.Width, f.Height, int(f.Track)+int(f.Sequence)), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := &res.Manifest
	if mutate != nil {
		mutate(m)
	}
	writeManifest(t, out, m)
	return out, m
}

func writeManifest(t *testing.T, out string, m *artifact.Manifest) {
	t.Helper()
	blob, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "manifest.json"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
}

func testTimeline(t *testing.T) *timeline.Timeline {
	t.Helper()
	var frames []model.Frame
	for tr := 1; tr <= 2; tr++ {
		for i := 0; i < 3; i++ {
			frames = append(frames, model.Frame{
				Track: model.TrackID(tr), Sequence: uint32(i + 1),
				PTS:   model.Timestamp(int64(i) * 40_000_000),
				Width: 32, Height: 32, Size: 8,
				Path:   "src.bin",
				SHA256: repeatHex(byte(tr*10 + i)),
				Color:  model.Color{Primaries: "bt709", Transfer: "bt709", Range: "full"},
			})
		}
	}
	tl, _, err := timeline.Merge(frames, timeline.MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return tl
}

func repeatHex(b byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 64)
	for i := range out {
		out[i] = hexdigits[int(b)%16]
	}
	return string(out)
}

func params() *artifact.Params {
	return &artifact.Params{Quality: 1, QuantStep: 1, FPS: 1, FillPolicy: "hold-last",
		Workers: 2, MaxCacheBytes: 16 << 20, MaxDimension: 4096}
}

func raw(w, h, seed int) pixel.Raw {
	r := pixel.Raw{W: w, H: h, Pix: make([]byte, w*h*3)}
	for i := range r.Pix {
		r.Pix[i] = byte((i*7 + seed*13) % 256)
	}
	return r
}

func TestVerifyCleanExport(t *testing.T) {
	out, m := fixture(t, nil)
	rep, err := Verify(out, Config{MaxDimension: 4096, MaxBytes: 10 << 20,
		MinLoudnessDBFS: -60, MaxVariance: 1e6})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Faults) != 0 {
		t.Fatalf("clean export has faults: %v", rep.Faults)
	}
	if rep.Frames != len(m.Frames) || rep.Segments != len(m.Segments) {
		t.Fatalf("counts = %d/%d", rep.Frames, rep.Segments)
	}
	if rep.PeakLoudness == 0 || rep.RunHash == "" {
		t.Fatalf("summary empty: %+v", rep)
	}
}

func TestVerifyDetectsTamperedFrame(t *testing.T) {
	out, m := fixture(t, nil)
	// Overwrite one artefact with different pixels; the hash must catch it.
	target := m.Frames[0]
	bad := raw(target.Width, target.Height, 999)
	data := encodePNG(t, bad)
	if err := os.WriteFile(filepath.Join(out, target.Path), data, 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := Verify(out, Config{MaxDimension: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(rep.Faults, model.CodeConstraintViolation) {
		t.Fatalf("tampered frame not detected: %v", rep.Faults)
	}
	for _, f := range rep.Faults {
		if f.Code == model.CodeConstraintViolation && f.Track == target.Track && f.Frame == target.Sequence {
			return
		}
	}
	t.Fatalf("fault does not name the tampered frame: %v", rep.Faults)
}

func TestVerifyDetectsManifestStatSwap(t *testing.T) {
	out, m := fixture(t, func(m *artifact.Manifest) {
		m.Frames[1].Stats.Variance += 50
	})
	rep, err := Verify(out, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(rep.Faults, model.CodeConstraintViolation) {
		t.Fatalf("statistic drift not detected: %v", rep.Faults)
	}
	if rep.Faults[0].Track != m.Frames[1].Track {
		t.Fatalf("fault track = %d", rep.Faults[0].Track)
	}
}

func TestVerifyDetectsMissingArtefact(t *testing.T) {
	out, m := fixture(t, nil)
	if err := os.Remove(filepath.Join(out, m.Frames[2].Path)); err != nil {
		t.Fatal(err)
	}
	rep, err := Verify(out, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(rep.Faults, model.CodeConstraintViolation) {
		t.Fatalf("missing artefact not detected: %v", rep.Faults)
	}
}

func TestVerifyDetectsPathEscape(t *testing.T) {
	out, _ := fixture(t, func(m *artifact.Manifest) {
		m.Frames[0].Path = "../escape.png"
	})
	rep, err := Verify(out, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(rep.Faults, model.CodeConstraintViolation) {
		t.Fatalf("escape not detected: %v", rep.Faults)
	}
}

func TestVerifyGeometryAndSizeCaps(t *testing.T) {
	out, _ := fixture(t, nil)
	rep, err := Verify(out, Config{MaxDimension: 16})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Faults) == 0 {
		t.Fatal("geometry cap not enforced")
	}
	out2, _ := fixture(t, nil)
	rep2, err := Verify(out2, Config{MaxBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep2.Faults) == 0 {
		t.Fatal("byte cap not enforced")
	}
}

func TestVerifyRejectsBadManifest(t *testing.T) {
	out := t.TempDir()
	if _, err := Verify(out, Config{}); err == nil {
		t.Fatal("missing manifest accepted")
	}
	if err := os.WriteFile(filepath.Join(out, "manifest.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(out, Config{}); err == nil {
		t.Fatal("malformed manifest accepted")
	}
}

func hasCode(faults model.Faults, code string) bool {
	for _, f := range faults {
		if f.Code == code {
			return true
		}
	}
	return false
}
