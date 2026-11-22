package stream

import (
	"os"
	"path/filepath"
	"testing"

	mp4 "github.com/abema/go-mp4"

	"mediaarchive/internal/fixtures"
)

// sttsEntry aliases the library type so table-validation tests read cleanly.
type sttsEntry = mp4.SttsEntry

func minimalSpec() fixtures.MP4Spec {
	spec := fixtures.MP4Spec{Timescale: 90000}
	for i := 0; i < 20; i++ {
		spec.Samples = append(spec.Samples, fixtures.MP4Sample{Size: 256, Delta: 3000, IsSync: i%5 == 0})
	}
	return spec
}

func buildSpecMP4(t *testing.T, spec fixtures.MP4Spec) []byte {
	t.Helper()
	return fixtures.BuildMP4(spec)
}

func writeSpec(t *testing.T, spec fixtures.MP4Spec, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, fixtures.BuildMP4(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}
