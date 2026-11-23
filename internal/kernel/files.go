package kernel

import (
	"io"
	"os"

	"mediad/internal/provenance"
	"mediad/internal/segment"
)

// VerifyFiles re-reads a manifest, its input and provenance record from disk
// and verifies them without any in-memory pipeline state. It is the standalone
// verifier used by "mediad verify".
func VerifyFiles(manifestPath, inputPath, provPath string) error {
	m, err := segment.LoadManifest(manifestPath)
	if err != nil {
		return provErr("load", err)
	}
	rec, err := provenance.LoadFile(provPath)
	if err != nil {
		return provErr("provenance", err)
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return internalErr("read", err)
	}
	sums := make([]string, len(m.Segments))
	for i, s := range m.Segments {
		sums[i] = s.Hash
	}
	if len(rec.Inputs) == 0 {
		return provErr("provenance", errNoInputs)
	}
	// Recompute the config digest from the manifest's own config block rather
	// than trusting its stored ConfigDigest, which would be circular.
	recomputedConfig := m.Config.Digest()
	if recomputedConfig != m.ConfigDigest {
		return provErr("config", errConfigDigestMismatch)
	}
	exp := provenance.Expected{
		ArtifactPath:   manifestPath,
		ArtifactSHA256: digestBytes(manifestBytes),
		ManifestSHA256: digestBytes(manifestBytes),
		ConfigDigest:   recomputedConfig,
		SegmentHashes:  sums,
		InputPaths: map[string]func() (io.ReadCloser, error){
			rec.Inputs[0].Path: func() (io.ReadCloser, error) { return os.Open(inputPath) },
		},
	}
	failures := provenance.Verify(rec, exp)
	if len(failures) > 0 {
		return provErr("provenance", failures[0])
	}
	return nil
}
