package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"mediapack/internal/manifest"
)

// cmdCorrupt flips one byte in a single published fragment so the local
// recovery path can be exercised deterministically. It is a test/demo helper;
// the core acceptance commands are demo/recover/verify.
func cmdCorrupt(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("corrupt", flag.ContinueOnError)
	fs.SetOutput(errOut)
	work := fs.String("work", "", "work directory")
	match := fs.String("id", "", "optional artifact id / path substring")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if strings.TrimSpace(*work) == "" {
		fmt.Fprintln(errOut, "--work is required")
		return 1
	}
	m, err := manifest.Load(*work)
	if err != nil {
		fmt.Fprintf(errOut, "load manifest: %v\n", err)
		return 1
	}
	var target *manifest.Artifact
	for i := range m.Artifacts {
		a := &m.Artifacts[i]
		if *match == "" || strings.Contains(a.ID, *match) || strings.Contains(a.RelPath, *match) {
			target = a
			break
		}
	}
	if target == nil {
		fmt.Fprintln(errOut, "no matching artifact")
		return 1
	}
	full, err := manifest.Resolve(*work, target.RelPath)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	b, err := os.ReadFile(full)
	if err != nil {
		fmt.Fprintf(errOut, "read fragment: %v\n", err)
		return 1
	}
	// Flip a byte inside the payload region (past the container header) while
	// keeping the file length stable, exactly the hash-failure case.
	idx := len(b) - 1
	if len(b) > 40 {
		idx = 40 + (len(b)-40)/2
	}
	b[idx] ^= 0xFF
	if err := os.WriteFile(full, b, 0o644); err != nil {
		fmt.Fprintf(errOut, "write fragment: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "corrupted=%s byte_offset=%d\n", target.RelPath, idx)
	return 0
}
