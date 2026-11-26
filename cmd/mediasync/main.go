package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"mediasync/internal/media"
	"mediasync/internal/output"
	"mediasync/internal/pipeline"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var code int
	switch os.Args[1] {
	case "run":
		code = runCommand(ctx, os.Args[2:])
	case "verify":
		code = verifyCommand(os.Args[2:])
	case "compare":
		code = compareCommand(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		code = 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		code = 2
	}
	os.Exit(code)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: media-sync <run|verify|compare> [flags]")
	fmt.Fprintln(os.Stderr, "  run --input DIR --out DIR [--budget-units N] [--max-bytes N] [--cancel-after-units N] [--resume]")
	fmt.Fprintln(os.Stderr, "  verify --out DIR")
	fmt.Fprintln(os.Stderr, "  compare --a DIR --b DIR")
}

func runCommand(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	input := fs.String("input", "", "input fixture directory")
	out := fs.String("out", "", "output directory")
	budget := fs.Int64("budget-units", 100000, "maximum processing units")
	maxBytes := fs.Int64("max-bytes", 1<<20, "maximum delivery bytes")
	cancelAfter := fs.Int64("cancel-after-units", 0, "simulate cancellation after N committed units")
	resume := fs.Bool("resume", false, "resume from checkpoint.json")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	rep, err := pipeline.Run(ctx, pipeline.Options{
		InputDir: *input, OutDir: *out, BudgetUnits: *budget,
		MaxBytes: *maxBytes, CancelAfterUnits: *cancelAfter, Resume: *resume,
	})
	if rep != nil {
		printRunReport(rep)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return media.ExitCode(err)
	}
	return 0
}

func printRunReport(r *pipeline.Report) {
	fmt.Printf("state=%s\n", r.State)
	if r.Tracks != 0 || r.Unique != 0 || r.Gaps != 0 {
		fmt.Printf("tracks=%d unique=%d duplicates=%d gaps=%d loss=%s\n",
			r.Tracks, r.Unique, r.Duplicates, r.Gaps, r.Loss)
	}
	if r.Frames > 0 || r.Files > 0 {
		fmt.Printf("frames=%d files=%d bytes=%d\n", r.Frames, r.Files, r.Bytes)
	}
	if r.RootHash != "" {
		fmt.Printf("root_hash=%s\n", r.RootHash)
	}
	fmt.Printf("units=%d budget_units=%d\n", r.UsedUnits, r.BudgetUnits)
	if r.State == "paused" {
		fmt.Printf("resume_track=%q resume_record=%d next_record=%d checkpoint=%s\n",
			r.ResumeTrack, r.ResumeRecord, r.NextRecord, r.CheckpointPath)
	}
}

func verifyCommand(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	out := fs.String("out", "", "output directory")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "verify: --out is required")
		return 2
	}
	catalog, report, err := output.Verify(*out)
	if report != nil {
		for _, msg := range report.Errors {
			fmt.Fprintf(os.Stderr, "verify-error: %s\n", msg)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return media.ExitCode(err)
	}
	fmt.Printf("verify=ok files=%d bytes=%d root_hash=%s state=%s\n",
		report.FileCount, report.TotalBytes, report.RootHash, catalog.State)
	for _, file := range catalog.Files {
		fmt.Printf("item=ok path=%s bytes=%d sha256=%s\n", file.Path, file.Bytes, file.SHA256)
	}
	return 0
}

func compareCommand(args []string) int {
	fs := flag.NewFlagSet("compare", flag.ContinueOnError)
	a := fs.String("a", "", "first output directory")
	b := fs.String("b", "", "second output directory")
	out := fs.String("out", "", "alias for --a when used with --other")
	other := fs.String("other", "", "alias for --b")
	first := fs.String("first", "", "alias for --a")
	second := fs.String("second", "", "alias for --b")
	left := fs.String("left", "", "alias for --a")
	right := fs.String("right", "", "alias for --b")
	expected := fs.String("expected", "", "alias for --a")
	actual := fs.String("actual", "", "alias for --b")
	against := fs.String("against", "", "alias for --b")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *a == "" && *out != "" {
		*a = *out
	}
	for _, candidate := range []string{*first, *left, *expected} {
		if *a == "" && candidate != "" {
			*a = candidate
		}
	}
	for _, candidate := range []string{*b, *other, *second, *right, *actual, *against} {
		if *b == "" && candidate != "" {
			*b = candidate
		}
	}
	positional := fs.Args()
	if *a == "" && *b == "" && len(positional) == 2 {
		*a, *b = positional[0], positional[1]
	} else if *b == "" && len(positional) == 1 {
		*b = positional[0]
	}
	if *a == "" || *b == "" {
		fmt.Fprintln(os.Stderr, "compare: --a and --b are required")
		return 2
	}
	cmp, err := output.CompareDirs(*a, *b)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return media.ExitCode(err)
	}
	fmt.Printf("equal=%t\n", cmp.Equal)
	fmt.Printf("root_hash_a=%s\nroot_hash_b=%s\n", cmp.RootA, cmp.RootB)
	return 0
}
