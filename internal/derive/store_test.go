package derive_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"media-check/internal/assets"
	"media-check/internal/derive"
)

func TestPutGetRoundTrip(t *testing.T) {
	store := openStore(t)
	src := assets.Seal("capture", []byte("source-bytes"))
	d, err := store.Put("report", src, []byte(`{"tracks":2}`))
	if err != nil {
		t.Fatal(err)
	}
	got, data, err := store.Get("report", src.ID)
	if err != nil {
		t.Fatalf("get after put: %v", err)
	}
	if got.Key != d.Key || string(data) != `{"tracks":2}` {
		t.Fatalf("round trip mismatch: %+v %s", got, data)
	}
}

func TestIdempotentPutReusesKey(t *testing.T) {
	store := openStore(t)
	src := assets.Seal("capture", []byte("source-bytes"))
	d1, _ := store.Put("mp4", src, []byte("artifact"))
	d2, _ := store.Put("mp4", src, []byte("artifact"))
	if d1.Key != d2.Key {
		t.Fatal("identical content must resolve to the same key")
	}
}

func TestChangedSourceInvalidatesDerivative(t *testing.T) {
	store := openStore(t)
	src1 := assets.Seal("capture", []byte("source-v1"))
	if _, err := store.Put("report", src1, []byte("v1-artifact")); err != nil {
		t.Fatal(err)
	}
	// Same kind, new source digest: the old derivative must not be returned.
	src2 := assets.Seal("capture", []byte("source-v2"))
	if _, _, err := store.Get("report", src2.ID); !errors.Is(err, derive.ErrMiss) {
		t.Fatalf("changed source must miss, got %v", err)
	}
}

func TestCorruptBlobDetected(t *testing.T) {
	store := openStore(t)
	src := assets.Seal("capture", []byte("source-bytes"))
	d, err := store.Put("report", src, []byte("artifact-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	// Tamper with the blob behind the store's back.
	blob := filepath.Join(store.Root(), d.File)
	if err := os.WriteFile(blob, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get("report", src.ID); err == nil {
		t.Fatal("content verification must reject a tampered blob")
	}
}

func TestInvalidateSourceRemovesArtifacts(t *testing.T) {
	store := openStore(t)
	src := assets.Seal("capture", []byte("source-bytes"))
	if _, err := store.Put("report", src, []byte("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put("mp4", src, []byte("b")); err != nil {
		t.Fatal(err)
	}
	n, err := store.InvalidateSource(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("invalidated %d artifacts, want 2", n)
	}
	if _, _, err := store.Get("report", src.ID); !errors.Is(err, derive.ErrMiss) {
		t.Fatalf("after invalidation get must miss, got %v", err)
	}
}

func openStore(t *testing.T) *derive.Store {
	t.Helper()
	store, err := derive.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}
