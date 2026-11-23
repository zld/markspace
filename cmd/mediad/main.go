// Command mediad is the terminal entry point for the broadcast media
// processing kernel. The demo subcommand generates deterministic MP4/MOV
// fixtures and runs them through probe, repair, remux and evidence.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

const usageText = `mediad — local MP4/MOV probe, repair and remux kernel

Usage:
  mediad demo [flags]

Flags:
  --seed int        deterministic fixture seed (default 23)
  --input-set str   fixture set name, currently only "generated" (default "generated")
  --remux           write a repackaged container for every fixture
  --verify          re-read output and verify round-trip metrics
  --json            emit the machine-readable report (always JSON-shaped)
  --work-dir str    optional directory for generated .mp4 artifacts

Exit codes:
  0  all in-policy checks passed
  1  input or processing failure
  2  verification metrics below threshold
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(1)
	}
	switch os.Args[1] {
	case "demo":
		os.Exit(runDemo(os.Args[2:]))
	case "-h", "--help", "help":
		fmt.Print(usageText)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n%s", os.Args[1], usageText)
		os.Exit(1)
	}
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	seed := fs.Int64("seed", 23, "deterministic fixture seed")
	inputSet := fs.String("input-set", "generated", "fixture set name")
	remuxFlag := fs.Bool("remux", false, "repackage each fixture")
	verify := fs.Bool("verify", false, "verify round-trip after remux")
	jsonOut := fs.Bool("json", false, "emit JSON report")
	workDir := fs.String("work-dir", "", "optional artifact directory")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *inputSet != "generated" {
		fmt.Fprintf(os.Stderr, "unsupported input-set %q: only \"generated\" is available\n", *inputSet)
		return 1
	}
	dir := ""
	if *workDir != "" {
		clean, err := safeWorkDir(*workDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "work-dir rejected: %v\n", err)
			return 1
		}
		dir = clean
	}
	opts := DemoOptions{
		Seed: *seed, InputSet: *inputSet, Remux: *remuxFlag,
		Verify: *verify, JSON: *jsonOut, WorkDir: dir,
	}
	code, _ := RunDemo(opts)
	return code
}

// safeWorkDir ensures the artifact directory is a local, non-traversing path.
func safeWorkDir(name string) (string, error) {
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("absolute paths are not allowed")
	}
	clean := filepath.Clean(name)
	if clean == ".." || filepath.IsAbs(clean) ||
		(len(clean) >= 3 && clean[:3] == ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path traversal segment")
	}
	if err := os.MkdirAll(clean, 0o755); err != nil {
		return "", err
	}
	return clean, nil
}
