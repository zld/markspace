package pipeline

import (
	"mediarun/internal/fault"
	"mediarun/internal/provenance"
)

// LoadManifest reads and validates a manifest file. It is exported for the CLI
// and for verification tests that compare two runs.
func LoadManifest(path string) (*provenance.Manifest, error) {
	m, err := provenance.Load(path)
	if err != nil {
		return nil, fault.Wrap(fault.StagePipeline, fault.KindIO, err, "load manifest")
	}
	if err := m.Validate(); err != nil {
		return nil, fault.New(fault.StagePipeline, fault.KindConfig, "%v", err)
	}
	return m, nil
}
