// Command regress is the terminal acceptance adapter for the deterministic,
// crash-safe media processing kernel. It generates synthetic multi-bitrate
// video and RTP audio, runs them through the staged pipeline and prints
// quantifiable A/V, loss-recovery, latency and manifest results.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"regress-kernel/internal/media"
	"regress-kernel/internal/pipeline"
)

func main() {
	caseName := flag.String("case", "normal", "fixture case: normal|corrupt|crash-after-publish")
	seed := flag.Int64("seed", 17, "deterministic fixture seed")
	out := flag.String("out", "", "workspace output directory")
	asJSON := flag.Bool("json", false, "emit the full program report as JSON")
	flag.Parse()

	code := run(*caseName, *seed, *out, *asJSON)
	os.Exit(code)
}

func run(caseName string, seed int64, out string, asJSON bool) int {
	cfg, crash, err := buildConfig(caseName, seed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "regress: %v\n", err)
		return 2
	}
	if out == "" {
		out = filepath.Join(os.TempDir(), "regress-"+caseName)
	}
	ws, err := pipeline.OpenWorkspace(out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "regress: workspace: %v\n", err)
		return 2
	}

	fx := media.Generate(cfg)
	result, err := ws.RunProgram(pipeline.RunOptions{
		CaseName: caseName, Seed: seed, Fx: fx, CrashAfterWrite: crash,
	})

	// A simulated crash on the first attempt is an expected, non-fatal event:
	// the operator reruns the same command and recovery completes the task.
	if crash && errors.Is(err, pipeline.ErrSimulatedCrash) {
		fmt.Println("simulated crash before state commit; rerun to recover")
		fmt.Printf("workspace: %s\n", out)
		return 0
	}
	if pipeline.IsValidation(err) {
		reportValidation(err)
		return 2
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "regress: %v\n", err)
		return 2
	}
	if _, err := ws.WriteReport(result); err != nil {
		fmt.Fprintf(os.Stderr, "regress: write report: %v\n", err)
		return 2
	}
	if asJSON {
		raw, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(raw))
		return 0
	}
	printHuman(result, out)
	return 0
}

// buildConfig maps the terminal case name onto a fully determined fixture.
func buildConfig(caseName string, seed int64) (media.FixtureConfig, bool, error) {
	cfg := media.DefaultConfig(caseName, seed)
	switch caseName {
	case "normal", "crash-after-publish":
		return cfg, caseName == "crash-after-publish", nil
	case "corrupt":
		// Concentrated, self-generated faults with explicit frame indexes.
		cfg.Corrupt = media.Corruption{
			TruncateAt: 3, BadChecksumAt: 7, DuplicateAt: 11,
			MissingAt: 15, OutOfRangeAt: 19,
		}
		return cfg, false, nil
	default:
		return cfg, false, fmt.Errorf("unknown case %q (want normal|corrupt|crash-after-publish)", caseName)
	}
}

// reportValidation prints every bad frame index so the failure is actionable.
func reportValidation(err error) {
	var ve *pipeline.ValidationError
	if !errors.As(err, &ve) {
		fmt.Fprintf(os.Stderr, "regress: %v\n", err)
		return
	}
	fmt.Println("frame validation failed; bad frame indexes:")
	for _, f := range ve.Report.Faults {
		fmt.Printf("  frame_index=%d kind=%s detail=%s\n", f.Index, f.Kind, f.Detail)
	}
	fmt.Printf("counters: duplicate=%d missing=%d out_of_range=%d bad_hash=%d\n",
		ve.Report.Counters.Duplicate, ve.Report.Counters.Missing,
		ve.Report.Counters.OutOfRange, ve.Report.Counters.BadHash)
}

func printHuman(r *pipeline.ProgramResult, out string) {
	fmt.Printf("case=%s seed=%d workspace=%s\n", r.CaseName, r.Seed, out)
	fmt.Printf("manifest_sha256=%s\n", r.ManifestSHA)
	fmt.Printf("sync_error_ms=%.3f recovery_rate=%.4f p95_latency_ms=%.4f\n",
		r.SyncErrorMs, r.RecoveryRate, r.P95LatencyMs)
	fmt.Printf("loss: injected=%d recovered=%d unrecoverable=%d\n",
		r.Loss.Injected, r.Loss.Recovered, r.Loss.Unrecoverable)
	fmt.Println("renditions:")
	for _, rd := range r.Renditions {
		fmt.Printf("  %s: segments=%d continuous=%t sync_samples=%d sync_error_ms=%.3f\n",
			rd.Rendition, rd.SegmentCount, rd.Continuous,
			rd.SampleCount, rd.SyncErrorMs)
	}
	if r.Resumed {
		fmt.Println("resumed_from_checkpoint=true")
	}
}
