// Command genfixtures writes generated MP4 fixtures for manual inspection.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zld/mediacore/internal/samplegen"
)

type manifestEntry struct {
	File           string `json:"file"`
	InjectedOffset int64  `json:"injected_offset"`
}

func main() {
	out := flag.String("out", "testdata/generated", "directory for generated MP4 fixtures")
	flag.Parse()

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatal(err)
	}
	entries, err := generate(*out)
	if err != nil {
		fatal(err)
	}
	manifest, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		fatal(err)
	}
	if err := os.WriteFile(filepath.Join(*out, "manifest.json"), manifest, 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("generated %d fixtures in %s\n", len(entries), *out)
}

func generate(out string) ([]manifestEntry, error) {
	var entries []manifestEntry
	write := func(name string, g *samplegen.Generated, offset int64) error {
		file := name + ".mp4"
		if err := os.WriteFile(filepath.Join(out, file), g.Data, 0o644); err != nil {
			return err
		}
		entries = append(entries, manifestEntry{File: file, InjectedOffset: offset})
		return nil
	}

	normal, err := samplegen.Generate(samplegen.NormalSpec(true))
	if err != nil {
		return nil, err
	}
	if err := write("normal", normal, 0); err != nil {
		return nil, err
	}

	noIndex, err := samplegen.Generate(samplegen.NormalSpec(false))
	if err != nil {
		return nil, err
	}
	if err := write("missing-index", noIndex, 0); err != nil {
		return nil, err
	}

	truncated, err := samplegen.Generate(samplegen.NormalSpec(true))
	if err != nil {
		return nil, err
	}
	truncOff, ok := samplegen.TruncateInSample(truncated, 2, 4, 32)
	if !ok {
		return nil, fmt.Errorf("truncate fixture")
	}
	if err := write("truncated-box", truncated, truncOff); err != nil {
		return nil, err
	}

	badLength, err := samplegen.Generate(samplegen.NormalSpec(true))
	if err != nil {
		return nil, err
	}
	badOff, ok := samplegen.CorruptBoxLength(badLength, 2, 4)
	if !ok {
		return nil, fmt.Errorf("bad-length fixture")
	}
	if err := write("bad-box-length", badLength, badOff); err != nil {
		return nil, err
	}

	boundary, err := samplegen.Generate(samplegen.NormalSpec(true))
	if err != nil {
		return nil, err
	}
	boundaryOff, ok := samplegen.TruncateInSample(boundary, 1, 5, 16)
	if !ok {
		return nil, fmt.Errorf("boundary fixture")
	}
	if err := write("boundary-cut", boundary, boundaryOff); err != nil {
		return nil, err
	}
	return entries, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "genfixtures:", err)
	os.Exit(1)
}
