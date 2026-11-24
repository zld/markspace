package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"mediapack/internal/batch"
	"mediapack/internal/demo"
	"mediapack/internal/metrics"
)

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	out := fs.String("out", "./tmp/demo", "output directory")
	chunk := fs.Int("chunk-samples", 8, "maximum samples per index chunk")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	absOut, err := filepath.Abs(*out)
	if err != nil {
		return exitError(err)
	}
	// Demo always starts from a clean index so its simulated lost checkpoint is
	// deterministic. Generated fixtures are overwritten.
	_ = os.RemoveAll(filepath.Join(absOut, "index"))

	cfg, paths, err := demo.Generate(absOut, *chunk)
	if err != nil {
		return exitError(err)
	}
	first, err := batch.Run(cfg)
	if err != nil {
		return exitError(err)
	}
	lost, err := batch.SimulateLostLastCheckpoint(absOut)
	if err != nil {
		return exitError(err)
	}
	recovered, err := batch.Run(cfg)
	if err != nil {
		return exitError(err)
	}
	report, _, verifyErr := metrics.VerifyDirectory(absOut)

	summary := map[string]any{
		"out":                    absOut,
		"fixtures":               paths,
		"deleted_checkpoint":     lost,
		"first_committed_chunks": first.Manifest.CommittedChunks,
		"final_committed_chunks": recovered.Manifest.CommittedChunks,
		"recovered_chunks":       recovered.Manifest.RecoveredChunks,
		"error_ranges":           recovered.Manifest.ErrorRanges,
		"verify_report":          report,
		"verify_ok":              verifyErr == nil,
	}
	b, _ := json.MarshalIndent(summary, "", "  ")
	fmt.Println(string(b))
	if verifyErr != nil {
		fmt.Fprintln(os.Stderr, "verify after demo failed:", verifyErr)
		return 1
	}
	return 0
}
