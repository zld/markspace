// Command mediad is the terminal entry point for the multi-camera delivery
// pipeline. It has two modes:
//
//	mediad demo   --seed N [--target-bitrate BPS] [--resume] [--corrupt KIND]
//	mediad verify --manifest PATH [--media-root DIR]
//
// The demo generates deterministic fixtures, runs the pipeline, prints a JSON
// report on stdout and exits 0. Corrupt media exits 2, bad arguments or
// manifests exit 1.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"mediad/internal/clock"
	"mediad/internal/fixture"
	"mediad/internal/pipeline"
)

func main() {
	code := run(os.Args[1:])
	os.Exit(code)
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage())
		return 1
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "verify":
		return runVerify(args[1:])
	case "-h", "--help", "help":
		fmt.Fprintln(os.Stdout, usage())
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s\n", args[0], usage())
		return 1
	}
}

func usage() string {
	return `mediad - multi-camera delivery pipeline

Usage:
  mediad demo   --seed N [--target-bitrate BPS] [--resume] [--corrupt KIND] [--workdir DIR]
  mediad verify --manifest PATH [--media-root DIR]

Demo flags:
  --seed            deterministic fixture seed (required)
  --target-bitrate  delivery target bits per second (default 8000000)
  --resume          reuse valid checkpoints from a previous run
  --corrupt         one of: truncated-table, edit-overrun, rtp-wrap,
                    missing-sync-flags, sparse-keyframes, dts-regression
  --infer-sync      derive keyframes when the source omits sync flags
  --workdir         working directory (default demo-work)

Exit codes:
  0 success   1 bad arguments or manifest   2 corrupt media or failed verification`
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	seed := fs.Int64("seed", -1, "deterministic fixture seed")
	target := fs.Int64("target-bitrate", 8_000_000, "delivery target bits per second")
	resume := fs.Bool("resume", false, "reuse valid checkpoints")
	corrupt := fs.String("corrupt", "", "corruption to inject")
	infer := fs.Bool("infer-sync", false, "infer keyframes when flags are absent")
	workdir := fs.String("workdir", "demo-work", "working directory")
	regenerate := fs.Bool("regenerate", false, "regenerate fixtures")
	resumeSkip := fs.Bool("resume-skip-fixtures", false, "")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *seed < 0 {
		fmt.Fprintf(os.Stderr, "--seed is required and must be non-negative\n\n%s\n", usage())
		return 1
	}
	if *target <= 0 || *target > 2_000_000_000 {
		fmt.Fprintf(os.Stderr, "--target-bitrate %d is out of range\n", *target)
		return 1
	}
	if !validCorruption(*corrupt) {
		fmt.Fprintf(os.Stderr, "unknown --corrupt %q; expected one of %v\n", *corrupt, fixture.Corruptions())
		return 1
	}
	_ = resumeSkip

	// Corruption is materialized into the source files, so a clean run and a
	// corrupt run must never share a work dir. An explicit --workdir that
	// already names a corruption (or a caller-supplied directory) is used as
	// is; otherwise a per-corruption subdirectory keeps runs isolated.
	dir := *workdir
	if *corrupt != "" && filepath.Base(dir) != "corrupt-"+*corrupt {
		dir = filepath.Join(dir, "corrupt-"+*corrupt)
	}
	opts := pipeline.Options{
		WorkDir: dir, Seed: *seed, TargetBps: *target, Corrupt: *corrupt,
		Resume: *resume, InferSync: *infer, Regenerate: *regenerate,
	}
	res, err := pipeline.Run(opts)
	if err != nil {
		return reportError(err)
	}
	out := struct {
		TargetBps          int64   `json:"target_bps"`
		ActualBps          int64   `json:"actual_bps"`
		BitrateDeltaPct    float64 `json:"bitrate_delta_pct"`
		Samples            int     `json:"samples"`
		Keyframes          int     `json:"keyframes"`
		TimestampRepairs   int     `json:"timestamp_repairs"`
		RTPWrapExtensions  int     `json:"rtp_wrap_extensions"`
		IndexCoveragePct   float64 `json:"index_coverage_pct"`
		QualityPSNRDb      float64 `json:"quality_psnr_db"`
		AlignmentErrorMs   float64 `json:"alignment_error_ms"`
		MaxIndexIntervalMs int64   `json:"max_index_interval_ms"`
		SparseKeyframes    bool    `json:"sparse_keyframes"`
		InputSHA256        string  `json:"input_sha256"`
		OutputSHA256       string  `json:"output_sha256"`
		Resumed            bool    `json:"resumed"`
		Seed               int64   `json:"seed"`
		Corrupt            string  `json:"corrupt,omitempty"`
	}{
		TargetBps: res.Report.TargetBps, ActualBps: res.Report.ActualBps,
		BitrateDeltaPct: res.Report.BitrateDeltaPct, Samples: res.Report.Samples,
		Keyframes: res.Report.Keyframes, TimestampRepairs: res.Report.TimestampRepairs,
		RTPWrapExtensions: res.Report.RTPWrapExtensions,
		IndexCoveragePct:  res.Report.IndexCoveragePct, QualityPSNRDb: res.Report.QualityPSNRDb,
		AlignmentErrorMs:   res.Report.AlignmentErrorMs,
		MaxIndexIntervalMs: res.Report.MaxIndexIntervalMs, SparseKeyframes: res.Report.SparseKeyframes,
		InputSHA256:  res.InputSHA,
		OutputSHA256: res.OutputSHA, Resumed: res.Resumed, Seed: *seed, Corrupt: *corrupt,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "encode report: %v\n", err)
		return 2
	}
	return 0
}

func runVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	manifestPath := fs.String("manifest", "", "manifest path")
	mediaRoot := fs.String("media-root", "", "root source paths resolve against")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *manifestPath == "" {
		fmt.Fprintln(os.Stderr, "--manifest is required")
		return 1
	}
	opts := pipeline.Options{ManifestPath: *manifestPath, MediaRoot: *mediaRoot,
		WorkDir: filepath.Dir(*manifestPath), ExternalManifest: true}
	res, err := pipeline.Run(opts)
	if err != nil {
		return reportError(err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]any{
		"digest": res.OutputSHA, "samples": res.Report.Samples,
		"coverage_pct":       res.Report.IndexCoveragePct,
		"bitrate_delta_pct":  res.Report.BitrateDeltaPct,
		"alignment_error_ms": res.Report.AlignmentErrorMs,
	})
	return 0
}

// reportError prints a structured error as JSON on stderr and maps it to the
// contract's exit code.
func reportError(err error) int {
	code := clock.ExitCode(err)
	// exit_code makes the intended process status explicit even when the
	// program is launched through `go run`, whose driver collapses every
	// non-zero child status to 1.
	payload := map[string]any{"error": err.Error(), "exit_code": code}
	if pe, ok := clock.As(err); ok {
		payload["stage"] = pe.Stage
		payload["error_code"] = pe.Code
		payload["track_id"] = pe.TrackID
		payload["raw_offset"] = pe.RawOffset
		if pe.Sample != 0 {
			payload["sample"] = pe.Sample
		}
		if pe.Camera != "" {
			payload["camera"] = pe.Camera
		}
		if pe.ManifestLine != 0 {
			payload["manifest_line"] = pe.ManifestLine
		}
	}
	b, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(os.Stderr, string(b))
	return code
}

func validCorruption(s string) bool {
	if s == "" {
		return true
	}
	for _, c := range fixture.Corruptions() {
		if c == s {
			return true
		}
	}
	return false
}
