package cache_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"mediarepair/internal/cache"
	"mediarepair/internal/source"
	"mediarepair/internal/testfixture"
)

func TestSamePathReplacementUsesContentFingerprint(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "same.png")
	a := make([]byte, 128)
	b := make([]byte, 128)
	b[0] = 1 // same size, different content
	if err := os.WriteFile(path, a, 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	infoA, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	fpA, err := cache.FileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	infoB, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if infoA.Size() != infoB.Size() || !infoA.ModTime().Equal(infoB.ModTime()) {
		t.Fatalf("test setup changed stat: size %d/%d mtime equal=%v", infoA.Size(), infoB.Size(), infoA.ModTime().Equal(infoB.ModTime()))
	}
	fpB, err := cache.FileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	if fpA == fpB {
		t.Fatal("content fingerprints must differ after same-path replacement")
	}

	c := cache.New()
	if _, ok := c.GetDecode(fpA); ok {
		t.Fatal("empty cache should miss")
	}
	c.PutDecode(fpA, nil)
	if _, ok := c.GetDecode(fpB); ok {
		t.Fatal("replacement content must not hit old decode cache")
	}
	if _, ok := c.GetDecode(fpA); !ok {
		t.Fatal("original content fingerprint should still hit")
	}
}

func TestPackagingInvalidationKeepsDecodeCache(t *testing.T) {
	root := t.TempDir()
	fx, err := testfixture.CreateStandard(root)
	if err != nil {
		t.Fatal(err)
	}
	st, err := source.ReadSampleTable(filepath.Join(root, fx.Manifest.MP4.Path), 1)
	if err != nil {
		t.Fatal(err)
	}
	fp1, err := cache.PackagingFingerprint(st)
	if err != nil {
		t.Fatal(err)
	}
	changed := *st
	changed.SampleOffsets = append([]source.SampleInfo(nil), st.SampleOffsets...)
	changed.SampleOffsets[0].Offset += 4096
	fp2, err := cache.PackagingFingerprint(&changed)
	if err != nil {
		t.Fatal(err)
	}
	if fp1 == fp2 {
		t.Fatal("packaging fingerprint must change with sample offsets")
	}

	c := cache.New()
	for i, b := range fx.FrameBytes {
		c.PutDecode(cache.ContentFingerprint(b), nil)
		_ = i
	}
	c.PutPackaging(fp1, st)
	if _, ok := c.GetPackaging(fp1); !ok {
		t.Fatal("first packaging lookup should hit after put")
	}
	if _, ok := c.GetPackaging(fp2); ok {
		t.Fatal("changed packaging fingerprint must miss")
	}
	for _, b := range fx.FrameBytes {
		if _, ok := c.GetDecode(cache.ContentFingerprint(b)); !ok {
			t.Fatal("unchanged frame bytes should retain decode cache")
		}
	}
	got := c.Snapshot()
	if got.PackagingInvalidations != 1 {
		t.Fatalf("packaging invalidations=%d want 1", got.PackagingInvalidations)
	}
	if got.DecodeHits != len(fx.FrameBytes) {
		t.Fatalf("decode hits=%d want %d", got.DecodeHits, len(fx.FrameBytes))
	}
}

func TestMetadataChangeOnlyInvalidatesIndex(t *testing.T) {
	root := t.TempDir()
	fx, err := testfixture.CreateStandard(root)
	if err != nil {
		t.Fatal(err)
	}
	fp1, err := cache.MetadataFingerprint(fx.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	c := cache.New()
	if !c.RequireIndexRebuild(fp1) {
		t.Fatal("first metadata fingerprint should require index build")
	}
	if c.RequireIndexRebuild(fp1) {
		t.Fatal("same metadata should not require rebuild")
	}
	changed := *fx.Manifest
	changed.Frames = append([]source.FrameRecord(nil), fx.Manifest.Frames...)
	changed.Frames[0].Color.Primaries = "bt2020"
	fp2, err := cache.MetadataFingerprint(&changed)
	if err != nil {
		t.Fatal(err)
	}
	if fp1 == fp2 {
		t.Fatal("metadata fingerprint should change")
	}
	if !c.RequireIndexRebuild(fp2) {
		t.Fatal("metadata change should invalidate index")
	}
	got := c.Snapshot()
	if got.IndexInvalidations != 1 {
		t.Fatalf("index invalidations=%d want 1", got.IndexInvalidations)
	}
	if got.DecodeHits != 0 || got.PackagingInvalidations != 0 {
		t.Fatalf("metadata change touched other caches: %+v", got)
	}
}
