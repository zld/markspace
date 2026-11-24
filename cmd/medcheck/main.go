package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"medcheck/internal/checkpoint"
	"medcheck/internal/pipeline"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		printUsage(os.Stderr)
		return 2
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "cases", "edge-cases":
		return runCases()
	case "checkpoint":
		return runCheckpoint(args[1:])
	case "help", "-h", "--help":
		printUsage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		printUsage(os.Stderr)
		return 2
	}
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	seed := fs.Int64("seed", 17, "deterministic fixture seed")
	loss := fs.String("loss", "", "comma-separated global packet indices to lose")
	reorder := fs.Int("reorder", 0, "number of disjoint same-stream reorders")
	every := fs.Int("checkpoint-every", 0, "save a checkpoint every N packets")
	truncateAt := fs.Int("truncate-at", 0, "interrupt after N observed packets")
	resume := fs.Bool("resume", false, "simulate checkpoint save, interruption, and resume")
	corruptCP := fs.Bool("corrupt-checkpoint", false, "corrupt the interruption checkpoint record")
	unrecoverable := fs.Bool("unrecoverable", false, "add an unresolved terminal video gap")
	cpPath := fs.String("checkpoint", "medcheck-checkpoint.jsonl", "checkpoint JSONL path")
	outPath := fs.String("output", "medcheck-output.mp4", "MP4 output path")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	lossIndices, err := parseLoss(*loss)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --loss: %v\n", err)
		return 2
	}
	cfg := pipeline.Config{
		Seed: *seed, Loss: lossIndices, Reorder: *reorder,
		CheckpointEvery: *every, TruncateAt: *truncateAt, Resume: *resume,
		CorruptCheckpoint: *corruptCP, Unrecoverable: *unrecoverable,
		CheckpointPath: *cpPath, OutputPath: *outPath,
	}
	if cfg.Resume && cfg.CheckpointEvery <= 0 {
		cfg.CheckpointEvery = 1
	}
	result, err := pipeline.RunDemo(cfg)
	if result != nil {
		writeJSON(result)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "medcheck: %v\n", err)
		if result != nil && result.ExitCode != 0 {
			return result.ExitCode
		}
		return 1
	}
	if result.ExitCode != 0 {
		return result.ExitCode
	}
	return 0
}

func runCases() int {
	cases := pipeline.RunCases()
	writeJSON(cases)
	for _, c := range cases {
		if !c.OK {
			return 1
		}
	}
	return 0
}

func runCheckpoint(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: medcheck checkpoint verify|corrupt --path FILE")
		return 2
	}
	switch args[0] {
	case "verify":
		fs := flag.NewFlagSet("checkpoint verify", flag.ContinueOnError)
		path := fs.String("path", "medcheck-checkpoint.jsonl", "checkpoint path")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		loaded, err := checkpoint.LoadLatestValid(*path)
		out := map[string]interface{}{
			"path": *path, "valid": err == nil,
			"valid_boundaries":   loaded.ValidBoundaries,
			"corrupt_records":    loaded.Corrupt,
			"recovered_boundary": loaded.State.Boundary,
		}
		writeJSON(out)
		if err != nil {
			fmt.Fprintf(os.Stderr, "checkpoint: %v\n", err)
			return 3
		}
		return 0
	case "corrupt":
		fs := flag.NewFlagSet("checkpoint corrupt", flag.ContinueOnError)
		path := fs.String("path", "medcheck-checkpoint.jsonl", "checkpoint path")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if err := checkpoint.CorruptLastRecord(*path); err != nil {
			fmt.Fprintf(os.Stderr, "checkpoint: %v\n", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown checkpoint command %q\n", args[0])
		return 2
	}
}

func parseLoss(s string) ([]int, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func writeJSON(v interface{}) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func printUsage(w *os.File) {
	fmt.Fprint(w, `medcheck - deterministic regional media kernel checker

usage:
  medcheck demo [--seed N] [--loss 4,9] [--reorder N]
                [--checkpoint-every N] [--truncate-at N] [--resume]
                [--corrupt-checkpoint] [--unrecoverable]
                [--checkpoint FILE] [--output FILE]
  medcheck cases
  medcheck checkpoint verify|corrupt --path FILE
`)
}
