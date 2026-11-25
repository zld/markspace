package samplegen

import (
	"os"
	"path/filepath"

	auditerrors "media-audit/internal/errors"
	"media-audit/internal/manifest"
	"media-audit/internal/media"
)

type FileSpec struct {
	Name string
	Data []byte
}

// Sample is a generated fixture plus the exact expectation used by --self-test.
type Sample struct {
	ID                   string
	Kind                 string
	StableKey            string
	Format               media.Format
	Files                []FileSpec
	Paths                []string
	ExpectedDamageOffset int64
	ExpectedCode         auditerrors.Code
	Normal               bool
}

type Bundle struct {
	Dir     string
	Samples []*Sample
}

func key(id string) string { return manifest.TaskKey("media-audit-selftest-v1", id) }

// Generate writes all deterministic fixtures and returns their expectations.
func Generate(dir string) (*Bundle, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	samples := allSamples()
	for _, s := range samples {
		s.Paths = nil
		for i := range s.Files {
			path := filepath.Join(dir, s.Files[i].Name)
			if err := os.WriteFile(path, s.Files[i].Data, 0o644); err != nil {
				return nil, err
			}
			s.Paths = append(s.Paths, path)
		}
	}
	return &Bundle{Dir: dir, Samples: samples}, nil
}
