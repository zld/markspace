package manifest

import (
	"path/filepath"
	"testing"

	"regress/internal/model"
)

func sampleEntries() []Entry {
	return []Entry{
		{Key: "z-key", Track: "t", Kind: model.KindID3, Tick: 30, Size: 3,
			ContentSha: "ccc"},
		{Key: "a-key", Track: "t", Kind: model.KindEmsg, Tick: 10, Size: 1,
			ContentSha: "aaa"},
		{Key: "m-key", Track: "t", Kind: model.KindSEI, Tick: 20, Size: 2,
			ContentSha: "bbb"},
	}
}

func TestRenderSortedAndStable(t *testing.T) {
	first := Render(sampleEntries())
	second := Render(sampleEntries())
	if string(first) != string(second) {
		t.Fatal("render not byte-stable across calls")
	}
	// Rows must come out in key order regardless of input ordering.
	out := string(first)
	ai := indexOf(out, "a-key")
	mi := indexOf(out, "m-key")
	zi := indexOf(out, "z-key")
	if !(ai < mi && mi < zi) {
		t.Fatalf("manifest not key-sorted:\n%s", out)
	}
	if Sha256(first) != Sha256(second) {
		t.Fatal("manifest hash unstable")
	}
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func TestMergeNoDuplicateKeys(t *testing.T) {
	existing := []Entry{
		{Key: "k1", ContentSha: "old", Tick: 1},
		{Key: "k2", ContentSha: "keep", Tick: 2},
	}
	incoming := []Entry{
		{Key: "k1", ContentSha: "new", Tick: 1}, // changed content replaces
		{Key: "k3", ContentSha: "add", Tick: 3},
		{Key: "k3", ContentSha: "add", Tick: 3}, // exact duplicate row
	}
	merged := Merge(existing, incoming)
	counts := map[string]int{}
	for _, e := range merged {
		counts[e.Key]++
	}
	if len(merged) != 3 || counts["k1"] != 1 || counts["k2"] != 1 ||
		counts["k3"] != 1 {
		t.Fatalf("merge produced wrong key set: %+v", merged)
	}
	for _, e := range merged {
		if e.Key == "k1" && e.ContentSha != "new" {
			t.Fatalf("k1 not replaced: %+v", e)
		}
	}
	// Rendering the merged set twice must be identical (deterministic).
	if string(Render(merged)) != string(Render(merged)) {
		t.Fatal("merged manifest not deterministic")
	}
}

func TestWriteParseRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.txt")
	entries := sampleEntries()
	data, sum, err := WriteAtomic(path, entries)
	if err != nil {
		t.Fatal(err)
	}
	if Sha256(data) != sum {
		t.Fatal("returned hash disagrees with rendered bytes")
	}
	parsed, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != len(entries) {
		t.Fatalf("parsed %d rows, want %d", len(parsed), len(entries))
	}
	// Re-rendering parsed rows must reproduce the exact file bytes.
	if got := Render(parsed); string(got) != string(data) {
		t.Fatalf("round trip changed bytes:\n%s\nvs\n%s", got, data)
	}
}

func TestParseMissingAndCorrupt(t *testing.T) {
	if entries, err := Parse(filepath.Join(t.TempDir(), "absent")); err != nil ||
		entries != nil {
		t.Fatalf("missing manifest should be empty, got %v %v", entries, err)
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.txt")
	if err := writeFile(bad, "wrong-header\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(bad); err == nil {
		t.Fatal("corrupt header accepted")
	}
}
