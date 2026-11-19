package scan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"streamcheck/internal/model"
)

func TestParseNameAcceptsValidNames(t *testing.T) {
	seg, err := ParseName("movie_1600000_42_1500.ts", "/tmp/movie_1600000_42_1500.ts")
	if err != nil {
		t.Fatalf("ParseName: %v", err)
	}
	if seg.Rate != 1600000 || seg.Number != 42 || seg.DurationMS != 1500 {
		t.Fatalf("unexpected segment: %+v", seg)
	}
}

func TestParseNameRejectsBadNames(t *testing.T) {
	cases := []string{
		"movie_1600000_42.ts",
		"movie 1600000_42_1000.ts",
		"movie_1600000_-42_1000.ts",
		"movie_1600000_42_0.ts",
		"movie_1600000_42_x.ts",
		"movie_0_42_1000.ts",
	}
	for _, name := range cases {
		if _, err := ParseName(name, name); err == nil {
			t.Errorf("%q was accepted, want error", name)
		}
	}
}

func TestScanDetectsDuplicatesAndHashes(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("seg_800000_0_1000.ts", "same")
	write("alt_800000_1_1000.ts", "same")
	write("dup_800000_1_1000.ts", "other")
	res, err := Scan(context.Background(), Options{Root: dir})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.Files != 3 {
		t.Fatalf("files = %d, want 3", res.Files)
	}
	if len(res.Issues) != 1 || res.Issues[0].Kind != model.IssueDuplicate || res.Issues[0].Number != 1 {
		t.Fatalf("duplicate number not reported: %+v", res.Issues)
	}
	for _, rate := range res.Rates {
		for _, seg := range rate.Segments {
			if len(seg.SHA256) != 64 {
				t.Fatalf("sha256 %q is not 64 hex chars", seg.SHA256)
			}
		}
	}
}

func TestScanRejectsSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "seg_800000_0_1000.ts")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "seg_800000_0_1000.ts")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := Scan(context.Background(), Options{Root: dir}); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	} else if !strings.Contains(err.Error(), "escapes root") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestScanHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Scan(ctx, Options{Root: t.TempDir()})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestMaxSegmentsBound(t *testing.T) {
	if model.MaxSegments != 100000 {
		t.Fatalf("MaxSegments = %d", model.MaxSegments)
	}
}
