package manifest

import (
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/model"
)

func TestAddSortedAndDuplicateRejected(t *testing.T) {
	m := New()
	entries := []*Entry{
		{SourceSHA: "ccc", Codec: "av1"},
		{SourceSHA: "aaa", Codec: "h264"},
		{SourceSHA: "bbb", Codec: "h265"},
	}
	for _, e := range entries {
		if err := m.Add(e); err != nil {
			t.Fatalf("add %s: %v", e.SourceSHA, err)
		}
	}
	if got := m.Entries[0].SourceSHA; got != "aaa" {
		t.Fatalf("entries not sorted: first=%s", got)
	}
	if !m.Has("bbb") || m.Has("zzz") {
		t.Fatal("Has lookup wrong")
	}
	if m.Get("aaa").Codec != "h264" {
		t.Fatal("Get returned wrong entry")
	}
	err := m.Add(&Entry{SourceSHA: "aaa", Codec: "h264"})
	se, ok := model.AsStructured(err)
	if !ok || se.Code != model.ErrDuplicateManifest {
		t.Fatalf("want duplicate error, got %v", err)
	}
	if len(m.Entries) != 3 {
		t.Fatal("duplicate append must not grow manifest")
	}
	if err := m.Add(nil); err == nil {
		t.Fatal("nil entry must be rejected")
	}
}

func TestDeterministicBytes(t *testing.T) {
	build := func() *Manifest {
		m := New()
		// Insert in different order in each run; output must still match.
		_ = m.Add(&Entry{SourceSHA: "z", Codec: "av1", StartNS: 10, EndNS: 20})
		_ = m.Add(&Entry{SourceSHA: "a", Codec: "h264", StartNS: 0, EndNS: 5})
		return m
	}
	b1, err := build().Marshal()
	if err != nil {
		t.Fatal(err)
	}
	b2, err := build().Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if string(b1) != string(b2) {
		t.Fatalf("non-deterministic manifest:\n%s\n%s", b1, b2)
	}
	d1, _ := build().Digest()
	d2, _ := build().Digest()
	if d1 != d2 || len(d1) != 64 {
		t.Fatal("manifest digest unstable")
	}
}

func TestWriteAtomicallyAndLoad(t *testing.T) {
	dir := t.TempDir()
	m := New()
	if err := m.Add(&Entry{SourceSHA: "abc", Codec: "h264", Timescale: 30000, SampleCount: 12}); err != nil {
		t.Fatal(err)
	}
	path, err := WriteAtomically(dir, "manifest.json", m)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "manifest.json" {
		t.Fatalf("unexpected path %s", path)
	}
	// No temp files left behind.
	if matches, _ := filepath.Glob(filepath.Join(dir, ".manifest-*")); len(matches) != 0 {
		t.Fatalf("temp files not cleaned: %v", matches)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Entries) != 1 || loaded.Entries[0].SourceSHA != "abc" {
		t.Fatal("loaded content mismatch")
	}
	// Byte-identical reload.
	raw1, _ := os.ReadFile(path)
	raw2, _ := loaded.Marshal()
	if string(raw1) != string(raw2) {
		t.Fatal("reloaded manifest does not re-marshal identically")
	}
}

func TestLoadRejectsBadVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"entries":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("unsupported version must fail")
	}
}

func TestLoadRejectsDuplicate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.json")
	doc := `{"version":1,"entries":[{"source_sha256":"x","codec":"h264"},{"source_sha256":"x","codec":"h264"}]}`
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	se, ok := model.AsStructured(err)
	if !ok || se.Code != model.ErrDuplicateManifest {
		t.Fatalf("want duplicate error on load, got %v", err)
	}
}
