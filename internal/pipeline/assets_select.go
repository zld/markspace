package pipeline

import (
	"os"
	"path/filepath"

	"archive/internal/normalize"
)

// ensureAssets reuses an existing deterministic asset directory unless the
// caller asked for regeneration or a file is missing.
func ensureAssets(opts Options) (*AssetPaths, error) {
	dir := filepath.Join(opts.OutDir, "assets")
	if !opts.Generate {
		paths, err := Generate(dir)
		if err == nil && allPresent(paths) {
			return paths, nil
		}
	}
	return Generate(dir)
}

func allPresent(p *AssetPaths) bool {
	for _, f := range []string{p.NormalMP4, p.NormalMXF, p.BoundaryMP4, p.CorruptMP4, p.InvalidMP4} {
		if _, err := os.Stat(f); err != nil {
			return false
		}
	}
	return true
}

// probeInvalid parses and normalises a sample expected to violate a rule.
func probeInvalid(path string) error {
	c, err := probeFile(path)
	if err != nil {
		return err
	}
	for _, t := range c.Tracks {
		if _, err := normalize.Resolve(t); err != nil {
			return err
		}
	}
	return nil
}
