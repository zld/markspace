package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"mediakey/internal/fixture"
	"mediakey/internal/manifest"
	"mediakey/internal/pipeline"
	"mediakey/internal/report"
)

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	chunks := fs.Int("chunks", 0, "number of chunks to generate")
	chunkSize := fs.Int("chunk-size", 0, "plaintext bytes per chunk")
	rotateEvery := fs.Int("rotate-every", 0, "rotate key every N chunks")
	inject := fs.String("inject", "", "comma-separated faults: expired,replay,tamper,out-of-window")
	workers := fs.Int("workers", 1, "concurrent verification workers")
	grace := fs.Int64("grace-seconds", 1800, "rotation overlap window in seconds")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *chunks <= 0 || *chunkSize <= 0 || *rotateEvery <= 0 {
		fmt.Fprintln(os.Stderr, "demo: -chunks, -chunk-size and -rotate-every are required and positive")
		return ExitUsage
	}
	if *rotateEvery > *chunks {
		fmt.Fprintln(os.Stderr, "demo: -rotate-every must not exceed -chunks")
		return ExitUsage
	}
	injections, err := fixture.ParseInjections(*inject)
	if err != nil {
		fmt.Fprintf(os.Stderr, "demo: %v\n", err)
		return ExitUsage
	}

	results, rotations, _, err := pipeline.Demo(pipeline.DemoConfig{
		Chunks:       *chunks,
		ChunkSize:    *chunkSize,
		RotateEvery:  *rotateEvery,
		Injections:   injections,
		Workers:      *workers,
		GraceSeconds: *grace,
	})
	if err != nil {
		return fail(err)
	}
	rep := report.Build("demo-asset-0001", rotations, results)
	return emit(rep)
}

func emit(rep *report.Report) int {
	raw, err := report.MarshalJSON(rep)
	if err != nil {
		return fail(err)
	}
	fmt.Print(string(raw))
	return ExitOK
}

// loadManifest reads and structurally validates a manifest file.
func loadManifest(path string) (*manifest.Manifest, int) {
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot read manifest: %v\n", err)
		return nil, ExitBadManifest
	}
	var m manifest.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		fmt.Fprintf(os.Stderr, "malformed manifest JSON: %v\n", err)
		return nil, ExitBadManifest
	}
	if err := m.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "invalid manifest: %s\n", manifest.ErrorCode(err))
		return nil, ExitBadManifest
	}
	return &m, ExitOK
}
