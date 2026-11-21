package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"streamcheck/internal/m3u8"
)

// buildTierDir writes a tier directory with a playlist and segment files.
func buildTierDir(t *testing.T, root, tier string, n int, segSize int) (map[string]string, m3u8.MediaPlaylist) {
	t.Helper()
	dir := filepath.Join(root, tier)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sidecar := make(map[string]string)
	pl := m3u8.MediaPlaylist{
		Version:        3,
		TargetDuration: 3 * time.Second,
		MediaSequence:  0,
		PlaylistType:   "VOD",
		Endlist:        true,
	}
	for i := 0; i < n; i++ {
		name := segName(i)
		payload := make([]byte, segSize)
		for j := range payload {
			payload[j] = byte(i ^ j)
		}
		if err := os.WriteFile(filepath.Join(dir, name), payload, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(payload)
		sidecar[name] = hex.EncodeToString(sum[:])
		pl.Segments = append(pl.Segments, m3u8.Segment{
			URI: name, Duration: 3 * time.Second,
		})
	}
	f, err := os.Create(filepath.Join(dir, "index.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := m3u8.WriteMedia(f, pl); err != nil {
		t.Fatal(err)
	}
	return sidecar, pl
}

func segName(i int) string {
	return "seg-" + pad(i) + ".ts"
}

func pad(i int) string {
	if i < 10 {
		return "0000" + string(rune('0'+i))
	}
	// Tests only need small indices; keep helper dependency-free.
	digits := []byte{}
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	for len(digits) < 5 {
		digits = append([]byte{'0'}, digits...)
	}
	return string(digits)
}

func TestCheckTierHealthy(t *testing.T) {
	root := t.TempDir()
	sidecar, _ := buildTierDir(t, root, "900k", 12, 3000)
	c := NewChecker(root, 50*time.Millisecond, 0.5)
	res := c.CheckTier(TierSpec{
		Name: "900k", BitrateBPS: 8000, SegmentMS: 3000, Sidecar: sidecar,
	})
	if res.HasFailures() {
		t.Fatalf("unexpected failures: %+v", res.Failures)
	}
	if res.Segments != 12 || res.DeviationMS != 0 {
		t.Fatalf("result: %+v", res)
	}
}

func TestCheckTierMissingSegment(t *testing.T) {
	root := t.TempDir()
	sidecar, _ := buildTierDir(t, root, "450k", 5, 1000)
	os.Remove(filepath.Join(root, "450k", segName(2)))
	c := NewChecker(root, 50*time.Millisecond, 0.5)
	res := c.CheckTier(TierSpec{Name: "450k", SegmentMS: 3000, Sidecar: sidecar})
	if !hasCode(res.Failures, FailMissingSegment) {
		t.Fatalf("expected missing_segment, got %+v", res.Failures)
	}
}

func TestCheckTierHashMismatch(t *testing.T) {
	root := t.TempDir()
	sidecar, _ := buildTierDir(t, root, "900k", 4, 1000)
	sidecar[segName(1)] = "deadbeef"
	c := NewChecker(root, 50*time.Millisecond, 0.5)
	res := c.CheckTier(TierSpec{Name: "900k", SegmentMS: 3000, Sidecar: sidecar})
	if !hasCode(res.Failures, FailHashMismatch) {
		t.Fatalf("expected hash_mismatch, got %+v", res.Failures)
	}
}

func TestCheckTierOutOfOrder(t *testing.T) {
	root := t.TempDir()
	_, _ = buildTierDir(t, root, "1800k", 4, 1000)
	// Rewrite the playlist with a media sequence that breaks consecutiveness.
	plPath := filepath.Join(root, "1800k", "index.m3u8")
	bad := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:3\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:3.000,\n" + segName(0) +
		"\n#EXTINF:3.000,\n" + segName(2) + "\n#EXT-X-ENDLIST\n"
	if err := os.WriteFile(plPath, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	c := NewChecker(root, 50*time.Millisecond, 0.5)
	res := c.CheckTier(TierSpec{Name: "1800k", SegmentMS: 3000})
	if !hasCode(res.Failures, FailOutOfOrder) {
		t.Fatalf("expected out_of_order, got %+v", res.Failures)
	}
}

func TestCheckTierDuplicate(t *testing.T) {
	root := t.TempDir()
	_, _ = buildTierDir(t, root, "450k", 3, 1000)
	plPath := filepath.Join(root, "450k", "index.m3u8")
	bad := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:3\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:3.000,\n" + segName(0) +
		"\n#EXTINF:3.000,\n" + segName(0) + "\n#EXT-X-ENDLIST\n"
	if err := os.WriteFile(plPath, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	c := NewChecker(root, 50*time.Millisecond, 0.5)
	res := c.CheckTier(TierSpec{Name: "450k", SegmentMS: 3000})
	if !hasCode(res.Failures, FailDuplicate) {
		t.Fatalf("expected duplicate, got %+v", res.Failures)
	}
}

func TestCheckTierDurationDeviation(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "450k")
	os.MkdirAll(dir, 0o755)
	pl := m3u8.MediaPlaylist{TargetDuration: 3 * time.Second, Endlist: true}
	for i := 0; i < 3; i++ {
		name := segName(i)
		os.WriteFile(filepath.Join(dir, name), make([]byte, 100), 0o644)
		pl.Segments = append(pl.Segments, m3u8.Segment{
			URI: name, Duration: 3500 * time.Millisecond,
		})
	}
	f, _ := os.Create(filepath.Join(dir, "index.m3u8"))
	m3u8.WriteMedia(f, pl)
	f.Close()
	c := NewChecker(root, 50*time.Millisecond, 0.5)
	res := c.CheckTier(TierSpec{Name: "450k", SegmentMS: 3000})
	if !hasCode(res.Failures, FailDuration) {
		t.Fatalf("expected duration deviation, got %+v", res.Failures)
	}
}

func TestCheckCrossTier(t *testing.T) {
	c := NewChecker(t.TempDir(), 50*time.Millisecond, 0.5)
	results := []TierResult{
		{Tier: "450k", Segments: 12, DurationMS: 36000},
		{Tier: "900k", Segments: 12, DurationMS: 36000},
		{Tier: "1800k", Segments: 11, DurationMS: 33000},
	}
	fails := c.CheckCrossTier(results)
	if !hasCode(fails, FailCrossTier) {
		t.Fatalf("expected cross-tier failure, got %+v", fails)
	}
}

func TestResolveRejectsTraversal(t *testing.T) {
	c := NewChecker(t.TempDir(), 0, 0)
	if _, err := c.resolve("..", "etc", "passwd"); err == nil {
		t.Fatal("path traversal must be rejected")
	}
	if _, err := c.resolve("/abs/path"); err == nil {
		t.Fatal("absolute path must be rejected")
	}
}

func hasCode(fails []Failure, code FailureCode) bool {
	for _, f := range fails {
		if f.Code == code {
			return true
		}
	}
	return false
}
