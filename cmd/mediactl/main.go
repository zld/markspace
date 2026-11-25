// Command mediactl is the terminal acceptance adapter for the media processing kernel.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"mediaflow/internal/batch"
	"mediaflow/internal/safepath"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "run":
		return runBatchCommand(args[1:], stdout, stderr)
	case "generate":
		return runGenerate(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `usage:
  mediactl demo --work DIR [--budget-mbps N] [--quality-min F] [--chunk-samples N]
  mediactl run --work DIR --manifest PATH [--batch-key KEY]
  mediactl generate --work DIR [--budget-mbps N] [--quality-min F] [--chunk-samples N]`)
}

type commonFlags struct {
	work         string
	budgetMbps   float64
	qualityMin   float64
	chunkSamples int
}

func bindCommon(fs *flag.FlagSet) *commonFlags {
	c := &commonFlags{}
	fs.StringVar(&c.work, "work", "./tmp", "working directory (all inputs/outputs stay below it)")
	fs.Float64Var(&c.budgetMbps, "budget-mbps", 18, "total bitrate budget in Mbps")
	fs.Float64Var(&c.qualityMin, "quality-min", 0.92, "minimum reproducible quality score [0,1]")
	fs.IntVar(&c.chunkSamples, "chunk-samples", 64, "maximum samples per chunk")
	return c
}

func (c *commonFlags) validate() int {
	if c.work == "" {
		return 2
	}
	if c.budgetMbps <= 0 {
		return 2
	}
	if c.qualityMin < 0 || c.qualityMin > 1 {
		return 2
	}
	if c.chunkSamples <= 0 {
		return 2
	}
	return 0
}

func prepareWork(work string) (string, error) {
	abs, err := filepath.Abs(work)
	if err != nil {
		return "", err
	}
	if _, err := safepath.Resolve(abs); err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", err
	}
	return abs, nil
}

func runGenerate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c := bindCommon(fs)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if code := c.validate(); code != 0 {
		fmt.Fprintln(stderr, "invalid generation parameters")
		return code
	}
	work, err := prepareWork(c.work)
	if err != nil {
		fmt.Fprintf(stderr, "prepare work: %v\n", err)
		return 2
	}
	_, manifestPath, err := generateFixtures(work, c)
	if err != nil {
		fmt.Fprintf(stderr, "generate fixtures: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "generated manifest %s\n", manifestPath)
	return 0
}

func runBatchCommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c := bindCommon(fs)
	manifest := fs.String("manifest", "", "manifest JSON path inside the working directory")
	batchKey := fs.String("batch-key", "", "optional batch key override")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if code := c.validate(); code != 0 || *manifest == "" {
		fmt.Fprintln(stderr, "invalid run parameters: --manifest is required and thresholds must be valid")
		return 2
	}
	work, err := prepareWork(c.work)
	if err != nil {
		fmt.Fprintf(stderr, "prepare work: %v\n", err)
		return 2
	}
	manifestPath, err := safepath.CheckWithin(work, *manifest)
	if err != nil {
		fmt.Fprintf(stderr, "manifest path rejected: %v\n", err)
		return 2
	}
	m, err := loadManifest(manifestPath)
	if err != nil {
		fmt.Fprintf(stderr, "manifest invalid: %v\n", err)
		return 2
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "budget-mbps":
			m.BudgetMbps = c.budgetMbps
		case "quality-min":
			m.QualityMin = c.qualityMin
		case "chunk-samples":
			m.ChunkSamples = c.chunkSamples
		}
	})
	summary := batch.RunManifest(m, batch.Options{WorkDir: work, BatchKey: *batchKey})
	summary.PrintText(stdout)
	summaryPath, _ := safepath.Resolve(work, "last-summary.json")
	if err := summary.WriteJSON(summaryPath); err != nil {
		fmt.Fprintf(stderr, "write summary: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "summary_json=%s\n", summaryPath)
	return summary.ExitCode
}
