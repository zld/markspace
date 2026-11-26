package render

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"mediasync/internal/ingest"
	"mediasync/internal/timeline"
)

func testTimeline(t *testing.T, text string) *timeline.Timeline {
	t.Helper()
	ref := int64(0)
	tl, err := timeline.Merge([]timeline.Input{{
		Spec: ingest.TrackSpec{ID: "subs", Kind: ingest.KindSubtitle},
		Subtitles: &ingest.SubtitleFile{Cues: []ingest.SubtitleCue{
			{Index: 1, StartMS: 0, EndMS: 1000, ReferenceStartMS: &ref, Text: text},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return tl
}

func TestRenderPNGStableHashAndTextSensitivity(t *testing.T) {
	tl := testTimeline(t, "你好, hello")
	specs, err := BuildSpecs(tl, []int64{0, 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 || !specs[0].Active || specs[0].Subtitle == "" {
		t.Fatalf("bad specs: %+v", specs)
	}
	dir1 := t.TempDir()
	results1, err := RenderAll(context.Background(), specs, dir1)
	if err != nil {
		t.Fatal(err)
	}
	dir2 := t.TempDir()
	results2, err := RenderAll(context.Background(), specs, dir2)
	if err != nil {
		t.Fatal(err)
	}
	if results1[0].SHA256 != results2[0].SHA256 {
		t.Fatal("same input produced different frame hash")
	}
	for _, r := range results1 {
		f, err := os.Open(filepath.Join(dir1, r.Path))
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(f)
		f.Close()
		if err != nil {
			t.Fatalf("frame %s is not PNG: %v", r.Path, err)
		}
		if img.Bounds().Dx() != Width || img.Bounds().Dy() != Height {
			t.Fatalf("bad dimensions %v", img.Bounds())
		}
		data, err := os.ReadFile(filepath.Join(dir1, r.Path))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if r.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("hash mismatch for %s", r.Path)
		}
	}

	changed := testTimeline(t, "再见, changed")
	changedSpecs, err := BuildSpecs(changed, []int64{0})
	if err != nil {
		t.Fatal(err)
	}
	changedResult, err := RenderFrame(changedSpecs[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if changedResult.SHA256 == results1[0].SHA256 {
		t.Fatal("text change did not change frame hash")
	}
}
