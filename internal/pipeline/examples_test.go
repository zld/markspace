package pipeline_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"mediad/internal/fixture"
	"mediad/internal/pipeline"
)

// Example shows the library entry point: generate a corpus, run the
// pipeline, inspect the summary and reuse the same output for a report.
func Example() {
	root, err := os.MkdirTemp("", "mediad-example-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(root)

	stream, err := fixture.Generate(fixture.DemoDefaults())
	if err != nil {
		panic(err)
	}
	for _, f := range stream.Files {
		path := filepath.Join(root, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			panic(err)
		}
		if err := os.WriteFile(path, f.Data, 0o644); err != nil {
			panic(err)
		}
	}

	runner := &pipeline.Runner{
		Root:   root,
		Params: pipeline.Parameters{Seed: 42, ClockRate: 90000, MasterTrack: "video"},
	}
	out, err := runner.Run(stream.Names())
	if err != nil && !errors.Is(err, pipeline.ErrDamaged) {
		panic(err)
	}
	fmt.Println(out.Summary.Frames >= 100, out.Summary.Cues >= 20, out.Summary.CorruptRanges > 0, len(out.Summary.SHA256) == 64)
	// Output: true true true true
}
