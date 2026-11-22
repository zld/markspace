package fixtures

import (
	"os"
	"path/filepath"
)

// Generate writes the track set and the corrupt samples under dir and
// returns the track file paths plus the corrupt sample paths by class.
func Generate(dir string, seed int64) (tracks []string, corrupt map[string]string, err error) {
	inDir := filepath.Join(dir, TracksDir)
	corDir := filepath.Join(dir, CorruptDir)
	if err := os.MkdirAll(inDir, 0o755); err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(corDir, 0o755); err != nil {
		return nil, nil, err
	}
	specs := trackSpecs()
	for i, s := range specs {
		var data []byte
		if s.format == "mp4" {
			data = buildMP4(seed, i, s)
		} else {
			data = buildMXF(seed, i, s)
		}
		p := filepath.Join(inDir, s.name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return nil, nil, err
		}
		tracks = append(tracks, p)
	}
	corrupt, err = buildCorrupt(corDir, seed, specs)
	if err != nil {
		return nil, nil, err
	}
	return tracks, corrupt, nil
}
