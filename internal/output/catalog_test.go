package output

import (
	"os"
	"path/filepath"
	"testing"

	"mediasync/internal/render"
	"mediasync/internal/timeline"
)

func TestCatalogRootHashStableAndVerify(t *testing.T) {
	dir := t.TempDir()
	payload := []byte("timeline\n")
	entry := EntryFromBytes("timeline.json", payload)
	if err := os.WriteFile(filepath.Join(dir, entry.Path), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	frames := []render.Result{{Index: 1, Path: "frames/frame-0001.png", Bytes: 3, SHA256: hashBytes([]byte("PNG"))}}
	if err := os.MkdirAll(filepath.Join(dir, "frames"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, frames[0].Path), []byte("PNG"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat := BuildCatalog(&timeline.Timeline{}, frames, []FileEntry{entry}, Budget{MaxBytes: 100})
	if cat.RootHash != RootHash(cat.Files) || len(cat.RootHash) != 64 {
		t.Fatalf("bad root hash %q", cat.RootHash)
	}
	if err := SaveCatalog(dir, cat); err != nil {
		t.Fatal(err)
	}
	if _, report, err := Verify(dir); err != nil || !report.OK {
		t.Fatalf("verify failed: %v report=%+v", err, report)
	}

	// Re-saving the same catalog must preserve the root hash.
	cat2 := BuildCatalog(&timeline.Timeline{}, frames, []FileEntry{entry}, Budget{MaxBytes: 100})
	if cat2.RootHash != cat.RootHash {
		t.Fatal("non-deterministic root hash")
	}
}

func TestVerifyDetectsTamperedHashAndExtraFile(t *testing.T) {
	dir := t.TempDir()
	data := []byte("hello")
	entry := EntryFromBytes("a.txt", data)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	cat := BuildCatalog(&timeline.Timeline{}, nil, []FileEntry{entry}, Budget{})
	if err := SaveCatalog(dir, cat); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Verify(dir); err == nil {
		t.Fatal("expected hash/size verification failure")
	}

	// Restore content, then add an unlisted file.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Verify(dir); err == nil {
		t.Fatal("expected unlisted file failure")
	}
}

func TestCompareDirs(t *testing.T) {
	entry := EntryFromBytes("a.txt", []byte("same"))
	ca := &Catalog{RootHash: RootHash([]FileEntry{entry}), Files: []FileEntry{entry}}
	cb := &Catalog{RootHash: ca.RootHash, Files: ca.Files}
	da, db := t.TempDir(), t.TempDir()
	if err := SaveCatalog(da, ca); err != nil {
		t.Fatal(err)
	}
	if err := SaveCatalog(db, cb); err != nil {
		t.Fatal(err)
	}
	cmp, err := CompareDirs(da, db)
	if err != nil || !cmp.Equal {
		t.Fatalf("cmp=%+v err=%v", cmp, err)
	}
}

func hashBytes(data []byte) string {
	return EntryFromBytes("x", data).SHA256
}
