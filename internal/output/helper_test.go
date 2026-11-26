package output

import (
	"os"
	"path/filepath"
	"testing"

	"replayproof/internal/fixtures"
	"replayproof/internal/parse"
)

// buildParsedMP4 writes a generated two-track MP4 into a temp dir and parses
// it. It is the shared source for clip-writing tests.
func buildParsedMP4(t *testing.T) (string, *parse.Asset) {
	t.Helper()
	gen, err := fixtures.GenerateMP4(fixtures.TrackSpec{}, fixtures.TrackSpec{})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "source.mp4")
	if err := os.WriteFile(path, gen.Bytes, 0o644); err != nil {
		t.Fatal(err)
	}
	asset, err := parse.ParseMP4(path)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	return path, asset
}
