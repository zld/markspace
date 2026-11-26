package fixture

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"liveregress/internal/fmp4"
	"liveregress/internal/manifest"
)

func TestAllCasesGenerateParseableTree(t *testing.T) {
	dir := t.TempDir()
	for _, c := range Cases {
		tree, err := GenerateOne(dir, c)
		if err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		if c == CaseBadPath || c == CaseBadMapping {
			// Refusal cases are intentionally unloadable; the pipeline tests
			// assert they fail with the right exit code.
			continue
		}
		master, err := manifest.LoadMaster(tree.Root, MasterName)
		if err != nil {
			t.Fatalf("%s master: %v", c, err)
		}
		if len(master.Variants) != 2 {
			t.Fatalf("%s variants = %d", c, len(master.Variants))
		}
		for _, v := range master.Variants {
			pl, err := manifest.LoadMedia(tree.Root, v.Path)
			if err != nil {
				t.Fatalf("%s %s: %v", c, v.Path, err)
			}
			if len(pl.Segments) != 3 {
				t.Fatalf("%s %s segments = %d", c, v.Path, len(pl.Segments))
			}
		}
		for _, layer := range tree.Layers {
			initPath := filepath.Join(tree.Root, layer.Path, InitName)
			r, err := fmp4.Open(initPath, layer.Path+"/"+InitName)
			if err != nil {
				t.Fatalf("%s %s: %v", c, layer.Name, err)
			}
			if _, err := r.ParseInit(); err != nil {
				t.Fatalf("%s %s init: %v", c, layer.Name, err)
			}
			r.Close()
			for s := 1; s <= 3; s++ {
				p := filepath.Join(tree.Root, layer.Path, fmt.Sprintf(SegmentPattern, s))
				if _, err := os.Stat(p); err != nil {
					t.Fatalf("%s missing %s", c, p)
				}
			}
		}
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	treeA, err := GenerateOne(a, CaseClean)
	if err != nil {
		t.Fatal(err)
	}
	treeB, err := GenerateOne(b, CaseClean)
	if err != nil {
		t.Fatal(err)
	}
	for _, layer := range DefaultLayers() {
		for s := 1; s <= 3; s++ {
			name := fmt.Sprintf(SegmentPattern, s)
			da, err := os.ReadFile(filepath.Join(treeA.Root, layer.Path, name))
			if err != nil {
				t.Fatal(err)
			}
			db, err := os.ReadFile(filepath.Join(treeB.Root, layer.Path, name))
			if err != nil {
				t.Fatal(err)
			}
			if Digest(da) != Digest(db) {
				t.Fatalf("non-deterministic fixture for %s/%s", layer.Path, name)
			}
		}
	}
}
