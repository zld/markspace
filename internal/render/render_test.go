package render

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"medialab/internal/artifact"
	"medialab/internal/model"
	"medialab/internal/pixel"
	"medialab/internal/timeline"
)

func testRaw(w, h int, seed int) pixel.Raw {
	r := pixel.Raw{W: w, H: h, Pix: make([]byte, w*h*3)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 3
			r.Pix[i] = byte((x*5 + y*3 + seed) % 256)
			r.Pix[i+1] = byte((x*2 + y*7 + seed) % 256)
			r.Pix[i+2] = byte((x + y*11 + seed) % 256)
		}
	}
	return r
}

func testTimeline(t *testing.T, tracks int, perTrack int) *timeline.Timeline {
	t.Helper()
	var frames []model.Frame
	for tr := 1; tr <= tracks; tr++ {
		for i := 0; i < perTrack; i++ {
			frames = append(frames, model.Frame{
				Track: model.TrackID(tr), Sequence: uint32(i + 1),
				PTS:   model.Timestamp(int64(i) * 40_000_000),
				Width: 32, Height: 32, Size: 64,
				Path:   fmt.Sprintf("t%d-%d.bin", tr, i),
				SHA256: hex.EncodeToString(mustHash([]byte{byte(tr), byte(i)})),
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

func mustHash(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

func testDecode(f model.Frame) (pixel.Raw, error) {
	return testRaw(f.Width, f.Height, int(f.Track)*13+int(f.Sequence)), nil
}

func testParams(step int) *artifact.Params {
	return &artifact.Params{
		Quality: 0.875, QuantStep: step, FPS: 1, FillPolicy: "hold-last",
		Workers: 2, MaxCacheBytes: 32 << 20, ReorderWindow: 8,
		MaxDimension: 4096, MaxBudgetMS: 500, MaxBytes: 1 << 20,
	}
}

func TestExportDeterministic(t *testing.T) {
	tl := testTimeline(t, 2, 3)
	var hashes []string
	for run := 0; run < 2; run++ {
		out := t.TempDir()
		res, err := Export(context.Background(), tl, Options{
			OutDir: out, Plan: testParams(8), Workers: 3, CacheCap: 32 << 20,
			Decode: testDecode,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Manifest.Frames) != 6 {
			t.Fatalf("frames = %d", len(res.Manifest.Frames))
		}
		if len(res.Manifest.Segments) != 2 {
			t.Fatalf("segments = %d", len(res.Manifest.Segments))
		}
		hashes = append(hashes, res.Manifest.Summary.RunHash)
	}
	if hashes[0] != hashes[1] {
		t.Fatalf("run hashes differ: %s vs %s", hashes[0], hashes[1])
	}
}

func TestExportWritesReadableArtefacts(t *testing.T) {
	out := t.TempDir()
	res, err := Export(context.Background(), testTimeline(t, 1, 2), Options{
		OutDir: out, Plan: testParams(8), Workers: 2, CacheCap: 32 << 20, Decode: testDecode,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range res.Manifest.Frames {
		data, err := os.ReadFile(filepath.Join(out, f.Path))
		if err != nil {
			t.Fatalf("read %s: %v", f.Path, err)
		}
		if got := sha256Hex(data); got != f.SHA256 {
			t.Fatalf("frame %s hash mismatch", f.Path)
		}
		if int64(len(data)) != f.Bytes {
			t.Fatalf("frame %s size mismatch", f.Path)
		}
		if !f.Fill && len(f.SourceSHA256) != 64 {
			t.Fatalf("frame %s missing source hash", f.Path)
		}
	}
	for _, s := range res.Manifest.Segments {
		if _, err := os.Stat(filepath.Join(out, s.Path)); err != nil {
			t.Fatalf("segment %s: %v", s.Path, err)
		}
	}
	if res.Manifest.Summary.TotalBytes == 0 || res.Manifest.Summary.RunHash == "" {
		t.Fatal("summary is empty")
	}
	blob, err := json.MarshalIndent(res.Manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "manifest.json"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "manifest.json")); err != nil {
		t.Fatal(err)
	}
}

func TestExportQuantisationChangesPixels(t *testing.T) {
	tl := testTimeline(t, 1, 1)
	lossless := t.TempDir()
	res1, err := Export(context.Background(), tl, Options{
		OutDir: lossless, Plan: testParams(1), Workers: 1, CacheCap: 32 << 20, Decode: testDecode,
	})
	if err != nil {
		t.Fatal(err)
	}
	quantised := t.TempDir()
	res2, err := Export(context.Background(), tl, Options{
		OutDir: quantised, Plan: testParams(64), Workers: 1, CacheCap: 32 << 20, Decode: testDecode,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res1.Manifest.Frames[0].SHA256 == res2.Manifest.Frames[0].SHA256 {
		t.Fatal("quantisation did not change the artefact")
	}
}

func TestExportHardFaultCancelsOnlyThatTrack(t *testing.T) {
	tl := testTimeline(t, 2, 3)
	decode := func(f model.Frame) (pixel.Raw, error) {
		if f.Track == 1 && f.Sequence == 2 {
			return pixel.Raw{}, model.NewFault(model.KindRender, model.CodePayloadCorrupt,
				f.Track, f.Sequence, "decoder rejected frame")
		}
		return testDecode(f)
	}
	out := t.TempDir()
	res, err := Export(context.Background(), tl, Options{
		OutDir: out, Plan: testParams(4), Workers: 1, CacheCap: 32 << 20, Decode: decode,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Faults.HasHard() {
		t.Fatal("hard fault not reported")
	}
	// Track 1 is cancelled at frame 2: that frame and the one job skipped
	// behind it become deterministic fills, while track 2 renders fully.
	if len(res.Manifest.Frames) != 6 {
		t.Fatalf("frames = %d want 6", len(res.Manifest.Frames))
	}
	fills, trackTwo := 0, 0
	for _, f := range res.Manifest.Frames {
		if f.Fill {
			fills++
		}
		if f.Track == 2 && !f.Fill {
			trackTwo++
		}
	}
	if fills != 2 {
		t.Fatalf("fills = %d want 2", fills)
	}
	if trackTwo != 3 {
		t.Fatalf("track 2 rendered %d frames want 3", trackTwo)
	}
	if len(res.Manifest.Segments) != 2 {
		t.Fatalf("segments = %d", len(res.Manifest.Segments))
	}
}

func TestExportKeepFilter(t *testing.T) {
	tl := testTimeline(t, 1, 4)
	out := t.TempDir()
	res, err := Export(context.Background(), tl, Options{
		OutDir: out, Plan: testParams(8), Workers: 1, CacheCap: 32 << 20, Decode: testDecode,
		Keep: func(e timeline.Event) bool { return e.Index%2 == 0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Manifest.Frames) != 2 {
		t.Fatalf("frames = %d want 2", len(res.Manifest.Frames))
	}
}

func TestExportRejectsBadInput(t *testing.T) {
	if _, err := Export(context.Background(), testTimeline(t, 1, 1), Options{}); err == nil {
		t.Fatal("empty options accepted")
	}
	tl := testTimeline(t, 1, 1)
	if _, err := Export(context.Background(), tl, Options{
		OutDir: t.TempDir(), Plan: testParams(1), Decode: testDecode,
		Keep: func(timeline.Event) bool { return false },
	}); err == nil {
		t.Fatal("empty selection accepted")
	}
}
