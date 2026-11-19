// Command streamcheck builds and verifies multi-bitrate HLS media playlists
// from locally transcoded segments.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"streamcheck/internal/fixture"
	"streamcheck/internal/manifest"
	"streamcheck/internal/model"
	"streamcheck/internal/report"
	"streamcheck/internal/scan"
	"streamcheck/internal/verify"
)

const usageText = `streamcheck - generate and verify local multi-bitrate HLS playlists

Usage:
  streamcheck build  --root DIR [--rates R1,R2,...] [--json]
  streamcheck verify --root DIR [--rates R1,R2,...] [--json]
                     [--missing-tolerance N] [--deviation-ms MS]
  streamcheck demo   --root DIR [--rates R1,R2,...] [--segments N]
                     [--drop RATE:NUMBER]... [--shuffle RATE]...

Segment files are named <slug>_<rate>_<number>_<duration_ms>.ts and live
directly under --root, next to one <rate>.m3u8 playlist per rate.

Exit codes:
  0  success / verification passed
  1  verification found problems
  2  invalid arguments or file operation failure
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return model.ExitError
	}
	switch args[0] {
	case "build":
		return runBuild(ctx, args[1:], stdout, stderr)
	case "verify":
		return runVerify(ctx, args[1:], stdout, stderr)
	case "demo":
		return runDemo(ctx, args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usageText)
		return model.ExitPass
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usageText)
		return model.ExitError
	}
}

func usageError(stderr io.Writer, format string, args ...any) int {
	fmt.Fprintf(stderr, "error: "+format+"\n\n", args...)
	fmt.Fprint(stderr, usageText)
	return model.ExitError
}

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }

func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}

func splitList(values []string) []string {
	var out []string
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func runBuild(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := fs.String("root", "", "directory holding segment files")
	ratesText := fs.String("rates", "", "comma-separated rates to build (default: every rate on disk)")
	asJSON := fs.Bool("json", false, "emit JSON instead of text")
	if err := fs.Parse(args); err != nil {
		return usageError(stderr, "%v", err)
	}
	if fs.NArg() > 0 {
		return usageError(stderr, "unexpected argument %q", fs.Arg(0))
	}
	if strings.TrimSpace(*root) == "" {
		return usageError(stderr, "--root is required")
	}
	var rates []int
	if strings.TrimSpace(*ratesText) != "" {
		parsed, err := fixture.ParseRates(*ratesText)
		if err != nil {
			return usageError(stderr, "%v", err)
		}
		rates = parsed
	}

	resolved, err := scan.ResolveRoot(*root)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return model.ExitError
	}
	scanned, err := scan.Scan(ctx, scan.Options{Root: resolved, Rates: rates})
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return model.ExitError
	}
	if missing := scan.MissingRates(rates, scanned); len(missing) > 0 {
		fmt.Fprintf(stderr, "error: no segment files found for rate(s) %v under %s\n", missing, resolved)
		return model.ExitError
	}
	if len(scanned.Rates) == 0 {
		fmt.Fprintf(stderr, "error: no segment files found under %s\n", resolved)
		return model.ExitError
	}
	if len(scanned.Issues) > 0 {
		for _, issue := range scanned.Issues {
			fmt.Fprintf(stderr, "error: %s\n", issue)
		}
		return model.ExitError
	}

	type built struct {
		Rate     int    `json:"rate"`
		Segments int    `json:"segment_count"`
		Duration int64  `json:"duration_ms"`
		Manifest string `json:"manifest"`
	}
	var results []built
	pending := make(map[string][]byte, len(scanned.Rates))
	for _, rate := range scanned.Rates {
		if err := ctx.Err(); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return model.ExitError
		}
		data, err := manifest.Build(rate.Rate, rate.Segments)
		if err != nil {
			fmt.Fprintf(stderr, "error: rate %d: %v\n", rate.Rate, err)
			return model.ExitError
		}
		path := filepath.Join(resolved, fmt.Sprintf("%d.m3u8", rate.Rate))
		pending[path] = data
		results = append(results, built{
			Rate: rate.Rate, Segments: rate.Count(), Duration: rate.DurationMS(), Manifest: path,
		})
	}
	if err := manifest.WriteManyAtomic(ctx, pending); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return model.ExitError
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Rate < results[j].Rate })

	if *asJSON {
		doc := struct {
			Status    string  `json:"status"`
			Root      string  `json:"root"`
			RateCount int     `json:"rate_count"`
			Rates     []built `json:"rates"`
		}{Status: "built", Root: resolved, RateCount: len(results), Rates: results}
		if err := report.WriteJSON(stdout, doc); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return model.ExitError
		}
		return model.ExitPass
	}
	fmt.Fprintf(stdout, "root=%s\nstatus=built rate_count=%d\n", resolved, len(results))
	for _, item := range results {
		fmt.Fprintf(stdout, "  rate=%d segment_count=%d duration_ms=%d manifest=%s\n",
			item.Rate, item.Segments, item.Duration, item.Manifest)
	}
	return model.ExitPass
}

func runVerify(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := fs.String("root", "", "directory holding segment files and manifests")
	ratesText := fs.String("rates", "", "comma-separated rates to restrict verification to")
	asJSON := fs.Bool("json", false, "emit JSON instead of text")
	tolerance := fs.Int("missing-tolerance", 0, "number of missing segments tolerated")
	threshold := fs.Int64("deviation-ms", verify.DefaultDeviationMS, "cross-rate cumulative deviation threshold in milliseconds")
	if err := fs.Parse(args); err != nil {
		return usageError(stderr, "%v", err)
	}
	if fs.NArg() > 0 {
		return usageError(stderr, "unexpected argument %q", fs.Arg(0))
	}
	if strings.TrimSpace(*root) == "" {
		return usageError(stderr, "--root is required")
	}
	if *tolerance < 0 {
		return usageError(stderr, "--missing-tolerance must be non-negative")
	}
	if *threshold <= 0 {
		return usageError(stderr, "--deviation-ms must be positive")
	}
	var rates []int
	if strings.TrimSpace(*ratesText) != "" {
		parsed, err := fixture.ParseRates(*ratesText)
		if err != nil {
			return usageError(stderr, "%v", err)
		}
		rates = parsed
	}

	resolved, err := scan.ResolveRoot(*root)
	if err != nil {
		return emitVerifyError(stdout, stderr, *asJSON, err)
	}
	scanned, err := scan.Scan(ctx, scan.Options{Root: resolved, Rates: rates})
	if err != nil {
		return emitVerifyError(stdout, stderr, *asJSON, err)
	}
	if missing := scan.MissingRates(rates, scanned); len(missing) > 0 {
		return emitVerifyError(stdout, stderr, *asJSON,
			fmt.Errorf("no segment files found for rate(s) %v under %s", missing, resolved))
	}
	res, err := verify.Run(ctx, resolved, scanned, verify.Options{
		MissingTolerance:     *tolerance,
		DeviationThresholdMS: *threshold,
		Rates:                rates,
	})
	if err != nil {
		return emitVerifyError(stdout, stderr, *asJSON, err)
	}
	doc := report.NewVerification(res)
	if *asJSON {
		if err := report.WriteJSON(stdout, doc); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return model.ExitError
		}
	} else if err := report.Text(stdout, doc); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return model.ExitError
	}
	if res.Failed() {
		return model.ExitFail
	}
	return model.ExitPass
}

func emitVerifyError(stdout, stderr io.Writer, asJSON bool, err error) int {
	if asJSON {
		_ = report.WriteJSON(stdout, report.Error{Status: string(model.StatusError), Message: err.Error()})
	}
	fmt.Fprintf(stderr, "error: %v\n", err)
	return model.ExitError
}

func runDemo(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := fs.String("root", "", "directory to populate (created if absent)")
	ratesText := fs.String("rates", "", "comma-separated rates, e.g. 800000,1600000,3200000")
	segments := fs.Int("segments", 12, "segments per rate")
	drops := &multiFlag{}
	shuffles := &multiFlag{}
	fs.Var(drops, "drop", "omit RATE:NUMBER, repeatable")
	fs.Var(shuffles, "shuffle", "write RATE's manifest entries out of order, repeatable")
	if err := fs.Parse(args); err != nil {
		return usageError(stderr, "%v", err)
	}
	if fs.NArg() > 0 {
		return usageError(stderr, "unexpected argument %q", fs.Arg(0))
	}
	if strings.TrimSpace(*root) == "" {
		return usageError(stderr, "--root is required")
	}
	rates, err := fixture.ParseRates(*ratesText)
	if err != nil {
		return usageError(stderr, "%v", err)
	}
	if *segments <= 0 || *segments > model.MaxSegments {
		return usageError(stderr, "--segments must be between 1 and %d", model.MaxSegments)
	}
	dropMap, err := fixture.ParseDrops(splitList(*drops))
	if err != nil {
		return usageError(stderr, "%v", err)
	}
	shuffleMap := make(map[int]bool)
	for _, value := range splitList(*shuffles) {
		var rate int
		if _, err := fmt.Sscanf(value, "%d", &rate); err != nil || rate <= 0 {
			return usageError(stderr, "--shuffle %q must be a positive rate", value)
		}
		shuffleMap[rate] = true
	}
	known := make(map[int]bool, len(rates))
	for _, rate := range rates {
		known[rate] = true
	}
	for rate := range dropMap {
		if !known[rate] {
			return usageError(stderr, "--drop rate %d is not in --rates", rate)
		}
	}
	for rate := range shuffleMap {
		if !known[rate] {
			return usageError(stderr, "--shuffle rate %d is not in --rates", rate)
		}
	}
	for rate, number := range dropMap {
		if number >= *segments {
			return usageError(stderr, "--drop %d:%d is beyond the %d generated segments", rate, number, *segments)
		}
	}

	abs, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return model.ExitError
	}
	plan := fixture.Plan{
		Root:     abs,
		Rates:    rates,
		Segments: *segments,
		Drop:     dropMap,
		Shuffle:  shuffleMap,
	}
	if err := fixture.Generate(ctx, plan); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintf(stderr, "error: demo cancelled; partial files may remain\n")
		} else {
			fmt.Fprintf(stderr, "error: %v\n", err)
		}
		return model.ExitError
	}
	fmt.Fprintf(stdout, "root=%s\nstatus=generated rate_count=%d segments_per_rate=%d\n",
		abs, len(rates), *segments)
	fmt.Fprintf(stdout, "next: streamcheck verify --root %s --json\n", abs)
	return model.ExitPass
}
