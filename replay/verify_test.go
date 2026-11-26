package replay

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"replayproof/internal/media"
)

func TestVerifyEndToEndMetrics(t *testing.T) {
	cfg := DefaultConfig()
	cfg.OutDir = filepath.Join(t.TempDir(), "proof")
	sum, err := RunVerify(context.Background(), cfg)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if sum.MapCount != 16 {
		t.Fatalf("map_count=%d", sum.MapCount)
	}
	if len(sum.Clips) != 2 {
		t.Fatalf("clips=%d", len(sum.Clips))
	}
	if len(sum.BadCases) != 3 {
		t.Fatalf("bad_cases=%d", len(sum.BadCases))
	}
	if sum.ByteErrors != 0 {
		t.Fatalf("byte_error=%d", sum.ByteErrors)
	}
	// The video GOP is 1 s, so alignment can move a clip start back by at
	// most one keyframe interval; the acceptance bound is 11112 us.
	if sum.MaxTimeErr > 11_112 {
		t.Fatalf("max_time_error_us=%d exceeds 11112", sum.MaxTimeErr)
	}
	if sum.QueuePeak > cfg.Queue {
		t.Fatalf("queue_peak=%d exceeds %d", sum.QueuePeak, cfg.Queue)
	}
	if !sum.ResumeEqual || !sum.Resumed || !sum.Canceled {
		t.Fatalf("resume/cancel verification: resumed=%v equal=%v canceled=%v",
			sum.Resumed, sum.ResumeEqual, sum.Canceled)
	}
	if len(sum.Digest()) != 64 {
		t.Fatalf("digest=%q", sum.Digest())
	}
	for _, bc := range sum.BadCases {
		switch bc.Name {
		case "truncated.mp4":
			if bc.Code != media.CodeTruncatedBox || bc.Offset != 16 {
				t.Fatalf("truncated case: %#v", bc)
			}
		case "outofrange.mxf":
			if bc.Code != media.CodeMXFOutOfRange || bc.Offset != 0 {
				t.Fatalf("mxf case: %#v", bc)
			}
		case "conflict.mxf":
			if bc.Code != media.CodeMETAConflict {
				t.Fatalf("conflict case: %#v", bc)
			}
			if !strings.Contains(bc.Detail, "rotation") {
				t.Fatalf("conflict detail should name the field: %q", bc.Detail)
			}
		default:
			t.Fatalf("unexpected bad case %q", bc.Name)
		}
	}
	// The published clips exist and are non-empty.
	for _, c := range sum.Clips {
		fi, err := os.Stat(c.Path)
		if err != nil || fi.Size() != c.Bytes {
			t.Fatalf("clip %s: %v", c.Path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(cfg.OutDir, "index.json")); err != nil {
		t.Fatalf("index not published: %v", err)
	}
}

func TestVerifySameSeedSameDigest(t *testing.T) {
	runs := make([]string, 3)
	for i := range runs {
		cfg := DefaultConfig()
		cfg.OutDir = filepath.Join(t.TempDir(), "proof")
		sum, err := RunVerify(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		runs[i] = sum.Digest()
	}
	for i := 1; i < len(runs); i++ {
		if runs[i] != runs[0] {
			t.Fatalf("run %d digest %s != %s", i, runs[i], runs[0])
		}
	}
}

func TestVerifyDifferentSeedDifferentDigest(t *testing.T) {
	a := DefaultConfig()
	a.Seed = 7
	a.OutDir = filepath.Join(t.TempDir(), "a")
	sumA, err := RunVerify(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	b := DefaultConfig()
	b.Seed = 8
	b.OutDir = filepath.Join(t.TempDir(), "b")
	sumB, err := RunVerify(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if sumA.Digest() == sumB.Digest() {
		t.Fatal("different seeds produced the same digest")
	}
}

func TestVerifyWritesSummaryWithDigest(t *testing.T) {
	cfg := DefaultConfig()
	cfg.OutDir = filepath.Join(t.TempDir(), "proof")
	sum, err := RunVerify(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := SummaryPath(cfg.OutDir)
	digest, err := WriteSummary(sum, path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "digest_sha256="+digest) {
		t.Fatalf("summary missing digest")
	}
	// Every sample line carries source time, clip time, byte offset and
	// deviation, as required for item-by-item review.
	if strings.Count(text, "map track=") != 16 {
		t.Fatalf("summary map lines=%d", strings.Count(text, "map track="))
	}
	for _, want := range []string{"time_err_us=", "src_off=", "clip_off=", "clip_us="} {
		if !strings.Contains(text, want) {
			t.Fatalf("summary missing %q", want)
		}
	}
}
