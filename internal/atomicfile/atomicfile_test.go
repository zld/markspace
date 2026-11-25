package atomicfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"assetcheck/internal/atomicfile"
)

func TestWriteFileAtomicAndChecksum(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "idx.json")
	payload := []byte(`{"ok":true}`)
	if err := atomicfile.WriteFile(p, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatal("payload mismatch")
	}
	want := atomicfile.SHA256Bytes(payload)
	fileSHA, err := atomicfile.SHA256File(p)
	if err != nil {
		t.Fatal(err)
	}
	if fileSHA != want {
		t.Fatalf("file sha %s != bytes sha %s", fileSHA, want)
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(filepath.Dir(p))
	for _, e := range entries {
		if len(e.Name()) >= 5 && e.Name()[:5] == ".tmp-" {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestOverwriteIsAtomic(t *testing.T) {
	p := filepath.Join(t.TempDir(), "m.json")
	if err := atomicfile.WriteFile(p, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.WriteFile(p, []byte("second-longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "second-longer" {
		t.Fatalf("overwrite content=%q", b)
	}
}
