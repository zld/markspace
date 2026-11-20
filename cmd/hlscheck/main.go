// Command hlscheck generates synthetic multi-bitrate transport streams,
// slices them into keyframe-aligned HLS segments, validates the output and
// injects deterministic faults for testing.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"hlscheck/internal/app"
	"hlscheck/internal/validate"
)

// Exit codes follow the specification:
//
//	0 success
//	1 argument or processing error
//	2 media consistency failure
const (
	exitOK          = 0
	exitProcess     = 1
	exitConsistency = 2
)

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(exitProcess)
	}
	var err error
	switch os.Args[1] {
	case "demo":
		err = runDemo(os.Args[2:])
	case "validate":
		err = runValidate(os.Args[2:])
	case "fault":
		err = runFault(os.Args[2:])
	case "-h", "--help", "help":
		usage(os.Stdout)
		os.Exit(exitOK)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(exitProcess)
	}
	if err != nil {
		if ce, ok := err.(*consistencyError); ok {
			fmt.Fprintln(os.Stderr, ce.Error())
			os.Exit(exitConsistency)
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(exitProcess)
	}
}

type consistencyError struct{ msg string }

func (e *consistencyError) Error() string { return e.msg }

// demo --work DIR --duration 11s --rates 350000,800000 --target 2s [--json]
func runDemo(args []string) error {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	work := fs.String("work", "", "working directory (sources and hls output are created below it)")
	duration := fs.String("duration", "11s", "stream duration, e.g. 11s")
	ratesFlag := fs.String("rates", "350000,800000", "comma-separated rendition bitrates in bps")
	target := fs.String("target", "2s", "target segment duration, e.g. 2s")
	frameRate := fs.Int("fps", 25, "source frame rate")
	asJSON := fs.Bool("json", false, "emit machine-readable JSON instead of text")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *work == "" {
		return fmt.Errorf("--work is required")
	}
	dur, err := time.ParseDuration(*duration)
	if err != nil {
		return fmt.Errorf("invalid --duration: %w", err)
	}
	tgt, err := time.ParseDuration(*target)
	if err != nil {
		return fmt.Errorf("invalid --target: %w", err)
	}
	rates, err := parseRates(*ratesFlag)
	if err != nil {
		return err
	}
	cfg := app.DemoConfig{
		WorkDir: *work, Duration: dur, Rates: rates,
		Target: tgt, FrameRate: *frameRate,
	}
	result, err := app.RunDemo(context.Background(), cfg)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result.Report); err != nil {
			return err
		}
	} else {
		renderTextReport(result.Report)
	}
	if !result.Report.OK {
		return &consistencyError{msg: "demo pipeline produced media consistency failures"}
	}
	return nil
}

// validate --root DIR --index PATH [--json]
func runValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	root := fs.String("root", "", "hls output root directory")
	indexPath := fs.String("index", "", "path to index.json (defaults to ROOT/index.json)")
	asJSON := fs.Bool("json", false, "emit machine-readable JSON instead of text")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *root == "" {
		return fmt.Errorf("--root is required")
	}
	idx := *indexPath
	if idx == "" {
		idx = filepathJoin(*root, "index.json")
	}
	report, _, err := app.RunValidate(*root, idx)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
	} else {
		renderTextReport(report)
	}
	if !report.OK {
		return &consistencyError{msg: "validation found media consistency failures"}
	}
	return nil
}

// fault --root DIR --index PATH --drop BPS:SEQ[,BPS:SEQ...] --swap BPS:A:B[...]
func runFault(args []string) error {
	fs := flag.NewFlagSet("fault", flag.ContinueOnError)
	root := fs.String("root", "", "hls output root directory")
	indexPath := fs.String("index", "", "path to index.json (defaults to ROOT/index.json)")
	dropFlag := fs.String("drop", "", "comma-separated BPS:SEQ segment deletions")
	swapFlag := fs.String("swap", "", "comma-separated BPS:A:B playlist position swaps")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *root == "" {
		return fmt.Errorf("--root is required")
	}
	idx := *indexPath
	if idx == "" {
		idx = filepathJoin(*root, "index.json")
	}
	drops, err := parseDrops(*dropFlag)
	if err != nil {
		return err
	}
	swaps, err := parseSwaps(*swapFlag)
	if err != nil {
		return err
	}
	if len(drops) == 0 && len(swaps) == 0 {
		return fmt.Errorf("at least one --drop or --swap specification is required")
	}
	dropped, swapped, err := app.RunFault(*root, idx, drops, swaps)
	if err != nil {
		return err
	}
	fmt.Printf("faults applied: dropped=[%s] swapped=[%s]\n",
		strings.Join(dropped, ","), strings.Join(swapped, ","))
	return nil
}

func parseRates(s string) ([]int, error) {
	var rates []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := strconv.Atoi(part)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("invalid bitrate %q", part)
		}
		rates = append(rates, v)
	}
	if len(rates) == 0 {
		return nil, fmt.Errorf("no bitrates parsed from %q", s)
	}
	return rates, nil
}

func parseDrops(s string) ([]validate.DropRequest, error) {
	var drops []validate.DropRequest
	for _, part := range splitCSV(s) {
		fields := strings.Split(part, ":")
		if len(fields) != 2 {
			return nil, fmt.Errorf("invalid --drop entry %q, want BPS:SEQ", part)
		}
		bps, err := strconv.Atoi(fields[0])
		if err != nil {
			return nil, fmt.Errorf("invalid bitrate in %q", part)
		}
		seq, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("invalid seq in %q", part)
		}
		drops = append(drops, validate.DropRequest{Bps: bps, Seq: seq})
	}
	return drops, nil
}

func parseSwaps(s string) ([]validate.SwapRequest, error) {
	var swaps []validate.SwapRequest
	for _, part := range splitCSV(s) {
		fields := strings.Split(part, ":")
		if len(fields) != 3 {
			return nil, fmt.Errorf("invalid --swap entry %q, want BPS:A:B", part)
		}
		nums := make([]int, 3)
		for i, f := range fields {
			v, err := strconv.Atoi(f)
			if err != nil {
				return nil, fmt.Errorf("invalid number in %q", part)
			}
			nums[i] = v
		}
		swaps = append(swaps, validate.SwapRequest{Bps: nums[0], A: nums[1], B: nums[2]})
	}
	return swaps, nil
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func filepathJoin(a, b string) string {
	return strings.TrimSuffix(a, "/") + "/" + b
}

// renderTextReport prints the stable, script-greppable per-rendition metric
// lines followed by every finding with its code and coordinates.
func renderTextReport(report *validate.Report) {
	for _, r := range report.Renditions {
		fmt.Printf("rendition_bps=%d segment_count=%d duration_ms=%d deviation_ms=%d failure_count=%d\n",
			r.Bps, r.SegmentCount, r.DurationMS, r.DeviationMS, r.FailureCount)
	}
	for _, f := range report.Findings {
		fmt.Printf("FINDING code=%s bps=%d seq=%d detail=%q\n", f.Code, f.Bps, f.Seq, f.Detail)
	}
	if report.OK {
		fmt.Println("result=OK")
	} else {
		fmt.Println("result=FAIL")
	}
}

func usage(w *os.File) {
	fmt.Fprintln(w, "hlscheck - local HLS slicer and validator")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "usage:")
	fmt.Fprintln(w, "  hlscheck demo     --work DIR --duration 11s --rates 350000,800000 --target 2s [--json] [--fps 25]")
	fmt.Fprintln(w, "  hlscheck validate --root DIR [--index FILE] [--json]")
	fmt.Fprintln(w, "  hlscheck fault    --root DIR [--index FILE] --drop BPS:SEQ,... --swap BPS:A:B,...")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "exit codes: 0 ok, 1 argument/processing error, 2 media consistency failure")
}
