package manifestio

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type sample struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func TestWriteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	if err := Write(path, sample{Name: "x", Count: 3}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var back sample
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Name != "x" || back.Count != 3 {
		t.Fatalf("round trip = %+v", back)
	}
	if raw[len(raw)-1] != '\n' {
		t.Fatal("manifest does not end with a newline")
	}
}

func TestWriteDeterministic(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "a.json")
	second := filepath.Join(dir, "b.json")
	if err := Write(first, sample{Name: "y", Count: 1}); err != nil {
		t.Fatal(err)
	}
	if err := Write(second, sample{Name: "y", Count: 1}); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(first)
	b, _ := os.ReadFile(second)
	if string(a) != string(b) {
		t.Fatal("identical manifests differ byte for byte")
	}
}

func TestWriteLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	if err := Write(path, sample{Name: "z"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "manifest.json" {
		t.Fatalf("directory has stray files: %v", entries)
	}
}

func TestWriteRejectsUnmarshalable(t *testing.T) {
	dir := t.TempDir()
	if err := Write(filepath.Join(dir, "m.json"), make(chan int)); err == nil {
		t.Fatal("unmarshalable value accepted")
	}
}
