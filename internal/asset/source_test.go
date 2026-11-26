package asset

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestImportIsContentAddressedAndStable(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("RIFF-evidence-bytes")
	s1, err := store.Import(bytes.NewReader(payload), int64(len(payload)), "audio/wav")
	if err != nil {
		t.Fatal(err)
	}
	s2, err := store.Import(bytes.NewReader(payload), int64(len(payload)), "audio/wav")
	if err != nil {
		t.Fatal(err)
	}
	if s1.SHA256 != s2.SHA256 || s1.Path != s2.Path {
		t.Fatalf("repeated imports differ: %+v %+v", s1, s2)
	}
	if got := DigestBytes(payload); got != s1.SHA256 {
		t.Fatalf("digest helper=%s source=%s", got, s1.SHA256)
	}
	if err := s1.Verify(); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(s1.Path) != filepath.Join(store.Root, "objects", "sha256", s1.SHA256[:2]) {
		t.Fatalf("unexpected object path %s", s1.Path)
	}
}

func TestImportFileDoesNotChangeOriginal(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "capture.wav")
	payload := bytes.Repeat([]byte{0, 1, 2, 3}, 16)
	if err := os.WriteFile(original, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	src, err := store.ImportFile(original, "audio/wav")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(original)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("original changed: err=%v equal=%v", err, bytes.Equal(got, payload))
	}
	if err := src.Verify(); err != nil {
		t.Fatal(err)
	}
}
