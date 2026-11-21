// Command streamcheck builds deterministic multi-tier HLS archives from
// synthetic MPEG-TS streams, injects faults and verifies the result locally.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"streamcheck/internal/build"
)

// Exit codes per the tool contract.
const (
	exitOK      = 0
	exitUsageIO = 1
	exitVerify  = 2
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		printUsage(os.Stderr)
		return exitUsageIO
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "verify":
		// verify is wired through the same pipeline for local file checks.
		return runDemo(args[1:])
	case "-h", "--help", "help":
		printUsage(os.Stdout)
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", args[0])
		printUsage(os.Stderr)
		return exitUsageIO
	}
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		out       = fs.String("out", "", "output directory (required)")
		duration  = fs.String("duration", "36s", "total stream duration, e.g. 36s")
		segment   = fs.String("segment", "3s", "target segment duration, e.g. 3s")
		tiersSpec = fs.String("tiers", "450k,900k,1800k", "comma-separated bitrate tiers")
		dropSpec  = fs.String("drop-segment", "", "tier:index segment to remove, e.g. 900k:4")
		swapSpec  = fs.String("swap-segments", "", "tier:a:b entries to swap, e.g. 1800k:2:3")
		asJSON    = fs.Bool("json", false, "emit a JSON report instead of text")
	)
	if err := fs.Parse(args); err != nil {
		return exitUsageIO
	}
	parsed, code := parseDemoFlags(out, duration, segment, tiersSpec, dropSpec, swapSpec)
	if code != exitOK {
		return code
	}
	parsed.jsonOutput = *asJSON

	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := build.Config{
		OutDir:      parsed.out,
		Duration:    parsed.duration,
		Segment:     parsed.segment,
		Tiers:       parsed.tiers,
		Faults:      parsed.faults,
		Concurrency: len(parsed.tiers),
	}
	result, err := build.Run(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitUsageIO
	}
	if parsed.jsonOutput {
		if err := result.Report.JSON(os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return exitUsageIO
		}
	} else {
		result.Report.Text(os.Stdout)
	}
	if result.Report.ValidationFailures > 0 {
		return exitVerify
	}
	return exitOK
}

func parseDemoFlags(out, duration, segment, tiersSpec, dropSpec, swapSpec *string) (demoArgs, int) {
	var a demoArgs
	if *out == "" {
		fmt.Fprintln(os.Stderr, "error: --out is required")
		return a, exitUsageIO
	}
	a.out = *out
	var err error
	if a.duration, err = time.ParseDuration(*duration); err != nil {
		fmt.Fprintf(os.Stderr, "error: bad --duration: %v\n", err)
		return a, exitUsageIO
	}
	if a.segment, err = time.ParseDuration(*segment); err != nil {
		fmt.Fprintf(os.Stderr, "error: bad --segment: %v\n", err)
		return a, exitUsageIO
	}
	if a.tiers, err = parseTiers(*tiersSpec); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return a, exitUsageIO
	}
	a.faults = map[string]build.TierFault{}
	if *dropSpec != "" {
		tier, idx, err := parseDropSpec(*dropSpec)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return a, exitUsageIO
		}
		f := ensureFault(a.faults, tier)
		f.HasDrop, f.DropSegment = true, idx
		a.faults[tier] = f
	}
	if *swapSpec != "" {
		tier, x, y, err := parseSwapSpec(*swapSpec)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return a, exitUsageIO
		}
		f := ensureFault(a.faults, tier)
		f.HasSwap, f.SwapA, f.SwapB = true, x, y
		a.faults[tier] = f
	}
	if err := validateFaultTiers(a.tiers, a.faults); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return a, exitUsageIO
	}
	return a, exitOK
}

func printUsage(w *os.File) {
	fmt.Fprintln(w, "usage: streamcheck demo --out DIR [flags]")
	fmt.Fprintln(w, "  --duration d         total stream duration (default 36s)")
	fmt.Fprintln(w, "  --segment d          target segment duration (default 3s)")
	fmt.Fprintln(w, "  --tiers list         bitrate tiers (default 450k,900k,1800k)")
	fmt.Fprintln(w, "  --drop-segment t:i   remove segment i of tier t")
	fmt.Fprintln(w, "  --swap-segments t:a:b swap playlist entries a and b of tier t")
	fmt.Fprintln(w, "  --json               emit machine-readable JSON")
	fmt.Fprintln(w, "exit codes: 0 pass, 1 parameter/I/O error, 2 verification failure")
}
