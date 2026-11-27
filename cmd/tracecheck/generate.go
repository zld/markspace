package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/local/tracecheck/internal/fixture"
)

// cmdGenerate writes the valid and corrupt fixture files into outDir.
func cmdGenerate(o options) int {
	if o.outDir == "" {
		fmt.Fprintln(os.Stderr, "argument error: --out is required with --generate")
		return exitInput
	}
	if err := os.MkdirAll(o.outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create output dir: %v\n", err)
		return exitInput
	}
	valid := fixture.Valid(fixture.DefaultOptions())
	corrupt, _ := fixture.TruncatedUnknownLength()
	files := map[string][]byte{
		"valid.mkv":   valid,
		"corrupt.mkv": corrupt,
	}
	for name, data := range files {
		path := filepath.Join(o.outDir, name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "cannot write %s: %v\n", name, err)
			return exitInput
		}
		fmt.Printf("wrote %s (%d bytes)\n", path, len(data))
	}
	return exitOK
}
