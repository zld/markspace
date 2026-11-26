package fixture_test

import (
	"os"
	"path/filepath"
	"testing"

	"example.com/archiveverify/internal/fixture"
)

func TestGeneratedManifestsAreDeterministic(t *testing.T) {
	ws1, err := fixture.Generate(t.TempDir(), "faults")
	if err != nil {
		t.Fatal(err)
	}
	ws2, err := fixture.Generate(t.TempDir(), "faults")
	if err != nil {
		t.Fatal(err)
	}
	b1, err := os.ReadFile(ws1.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := os.ReadFile(ws2.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(b1) != string(b2) {
		t.Fatal("fault manifest generation is not byte-deterministic")
	}
	if filepath.Base(ws1.ManifestPath) != "manifest.json" {
		t.Fatalf("unexpected manifest path %s", ws1.ManifestPath)
	}
}
