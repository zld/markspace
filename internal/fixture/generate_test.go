package fixture

import (
	"os"
	"path/filepath"
	"testing"

	"subtitle/internal/parser"
)

func TestWriteAndParse(t *testing.T) {
	dir := t.TempDir()
	paths, err := Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("want 2 fixtures, got %d", len(paths))
	}
	raw, err := os.ReadFile(filepath.Join(dir, "scene.srt"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := parser.ParseSRT("scene.srt", string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Cues) != 4 {
		t.Fatalf("scene.srt must contain 4 cues, got %d", len(doc.Cues))
	}
	if doc.Cues[2].Text != "third cue\nfirst overlap" {
		t.Fatalf("multi-line cue body wrong: %q", doc.Cues[2].Text)
	}
}
