package pipeline

import (
	"path/filepath"
	"strings"

	"replayctl/internal/fault"
	"replayctl/internal/journal"
	"replayctl/internal/validate"
)

// Verify reads the manifest and independently rechecks the delivered artifact.
func Verify(dir string) (*journal.Manifest, *validate.Metrics, error) {
	root, err := journal.ValidateOutputPath(dir)
	if err != nil {
		return nil, nil, err
	}
	manifest, err := journal.ReadManifest(root)
	if err != nil {
		return nil, nil, err
	}
	if manifest.Artifact == "" || strings.Contains(manifest.Artifact, "..") {
		return nil, nil, fault.New(fault.Validation, fault.StageValidate, root, -1, -1, "unsafe manifest artifact path")
	}
	path := filepath.Join(root, filepath.FromSlash(manifest.Artifact))
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, nil, fault.New(fault.Validation, fault.StageValidate, path, -1, -1, "artifact escapes output directory")
	}

	metrics, err := validate.MeasureFile(path)
	if err != nil {
		return nil, nil, err
	}
	if metrics.SHA256 != manifest.SHA256 || manifest.Metrics.SHA256 != manifest.SHA256 {
		return nil, nil, fault.New(fault.Validation, fault.StageValidate, path, -1, -1, "artifact hash does not match manifest")
	}
	metrics.VideoAlignErrorMS = manifest.Metrics.VideoAlignErrorMS
	metrics.AudioBoundaryErrorSamples = manifest.Metrics.AudioBoundaryErrorSamples
	expected := validate.Expectations{
		Frames:                  manifest.Metrics.Frames,
		Pixels:                  manifest.Metrics.Pixels,
		AudioSamples:            manifest.Metrics.AudioSamples,
		VideoAlignMaxMS:         20,
		AudioBoundaryMaxSamples: 1,
	}
	if err := metrics.Check(expected); err != nil {
		return nil, nil, err
	}
	if !metrics.NearPeak() {
		return nil, nil, fault.New(fault.Validation, fault.StageValidate, path, -1, -1, "peak %.2f dBFS outside tolerance", metrics.PeakDBFS)
	}
	return manifest, metrics, nil
}
