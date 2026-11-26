package fixture

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateCreatesAllSamples(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fixtures")
	res, err := Generate(Options{Dir: dir, Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"normal.json", "bad-range.json", "bad-jump.json", "interrupt.json",
		"wrap.mp4", "edit.mp4", "jump.mp4", "bad-range.mp4", "bad-jump.mp4",
	}
	for _, name := range want {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	if len(res.Manifests) != 4 || len(res.Media) != 5 {
		t.Fatalf("result counts manifests=%d media=%d", len(res.Manifests), len(res.Media))
	}
}
