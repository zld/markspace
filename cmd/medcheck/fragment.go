package main

import (
	"os"
	"path/filepath"

	"medkernel/pipeline"
)

// writeSampleFragment exercises the fragmented-MP4 container boundary code:
// it writes an init segment and one fragment with deterministic sample bytes.
func writeSampleFragment(path, scenario string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return // fragment output is best-effort; journal metrics are authoritative
	}
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	fw := pipeline.NewFragmentWriter(f, 1)
	if err := fw.WriteInitSegment(48000); err != nil {
		return
	}
	samples := [][]byte{
		[]byte(scenario + "-s0"),
		[]byte(scenario + "-s1"),
	}
	_ = fw.WriteFragment(1, samples, 480)
}
