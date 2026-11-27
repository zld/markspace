// Command mediad is the terminal front end for the resumable media processing
// kernel. It generates synthetic multi-camera audio fixtures, runs them through
// gap-aware timeline stages, and verifies the resulting manifest.
package main

import (
	"fmt"
	"os"
	"strings"

	"mediad/internal/fixture"
	"mediad/internal/manifest"
	"mediad/internal/model"
	"mediad/internal/pipeline"
	"mediad/internal/quality"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	var err error
	switch os.Args[1] {
	case "demo":
		err = runDemo(os.Args[2:])
	case "resume":
		err = runResume(os.Args[2:])
	case "verify":
		err = runVerify(os.Args[2:])
	case "selfcheck":
		if len(os.Args) < 3 {
			err = model.ErrUsage.At("", 0, "cli", 0, "selfcheck needs a kind")
			break
		}
		err = runSelfCheck(os.Args[2])
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		err = model.ErrUsage.At("", 0, "cli", 0, "unknown command "+os.Args[1])
	}
	if err != nil {
		me := model.AsError(err)
		fmt.Fprintln(os.Stderr, "ERROR "+me.Error())
		os.Exit(model.ExitStatus(err))
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `mediad - resumable media processing kernel

usage:
  mediad demo   --fixture generated --out DIR [--scenario S] [--interrupt-after stage:NAME]
                [--target-bitrate BPS] [--min-quality Q]
                [--cpu-budget-ns N] [--byte-budget N] [--max-delivery-bytes N]
  mediad resume --out DIR
  mediad verify --out DIR

scenarios: healthy (default), duplicate, missing, truncated-aac, bad-opus-toc
  mediad selfcheck graph-cycle | time-overflow
exit codes: 0 success, 1 parameter error, 2 input/processing failure
`)
}

// flags is a minimal deterministic flag parser (no global flag state).
type flags struct {
	out            string
	fixture        string
	scenario       string
	interruptAfter string
	targetBitrate  uint64
	minQuality     float64
	cpuBudgetNS    int64
	byteBudget     int64
	maxDelivery    int64
}

func parseFlags(args []string) (flags, error) {
	f := flags{
		scenario:      fixture.ScenHealthy,
		targetBitrate: 80_000,
		minQuality:    0.90,
		cpuBudgetNS:   1 << 40,
		byteBudget:    1 << 40,
		maxDelivery:   1 << 40,
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		need := func() (string, error) {
			if i+1 >= len(args) {
				return "", model.ErrUsage.At("", 0, "cli", 0, "flag "+a+" needs a value")
			}
			i++
			return args[i], nil
		}
		var v string
		var perr error
		switch {
		case a == "--out":
			if v, perr = need(); perr != nil {
				return f, perr
			}
			f.out = v
		case a == "--fixture":
			if v, perr = need(); perr != nil {
				return f, perr
			}
			f.fixture = v
		case a == "--scenario":
			if v, perr = need(); perr != nil {
				return f, perr
			}
			f.scenario = v
		case strings.HasPrefix(a, "--interrupt-after"):
			// Accept both "--interrupt-after stage:normalize" and "=stage:..".
			if strings.Contains(a, "=") {
				f.interruptAfter = strings.SplitN(a, "=", 2)[1]
			} else if v, perr = need(); perr == nil {
				f.interruptAfter = v
			} else {
				return f, perr
			}
		case a == "--target-bitrate":
			if v, perr = need(); perr != nil {
				return f, perr
			}
			if f.targetBitrate, perr = parseU64(v, a); perr != nil {
				return f, perr
			}
		case a == "--min-quality":
			if v, perr = need(); perr != nil {
				return f, perr
			}
			if f.minQuality, perr = parseFloat(v, a); perr != nil {
				return f, perr
			}
		case a == "--cpu-budget-ns":
			if v, perr = need(); perr != nil {
				return f, perr
			}
			if f.cpuBudgetNS, perr = parseI64(v, a); perr != nil {
				return f, perr
			}
		case a == "--byte-budget":
			if v, perr = need(); perr != nil {
				return f, perr
			}
			if f.byteBudget, perr = parseI64(v, a); perr != nil {
				return f, perr
			}
		case a == "--max-delivery-bytes":
			if v, perr = need(); perr != nil {
				return f, perr
			}
			if f.maxDelivery, perr = parseI64(v, a); perr != nil {
				return f, perr
			}
		default:
			return f, model.ErrUsage.At("", 0, "cli", 0, "unknown flag "+a)
		}
	}
	if f.out == "" {
		return f, model.ErrUsage.At("", 0, "cli", 0, "--out is required")
	}
	if f.minQuality < 0 || f.minQuality > 1 {
		return f, model.ErrUsage.At("", 0, "cli", 0,
			"--min-quality must lie in [0,1]")
	}
	if f.targetBitrate == 0 {
		return f, model.ErrUsage.At("", 0, "cli", 0,
			"--target-bitrate must be positive")
	}
	return f, nil
}

func runDemo(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	if f.fixture != "generated" {
		return model.ErrUsage.At("", 0, "cli", 0,
			"only --fixture generated is supported in this build")
	}
	cfg := pipeline.Config{
		OutDir: f.out, Scenario: f.scenario, TargetBitrate: f.targetBitrate,
		MinQuality: f.minQuality, CPUBudgetNS: f.cpuBudgetNS,
		ByteBudget: f.byteBudget, MaxDeliveryB: f.maxDelivery,
	}
	if f.interruptAfter != "" {
		name, ok := strings.CutPrefix(f.interruptAfter, "stage:")
		if !ok {
			return model.ErrUsage.At("", 0, "cli", 0,
				"--interrupt-after expects stage:<name>")
		}
		cfg.InterruptAfter = name
	}
	p, err := pipeline.New(cfg, "demo-run")
	if err != nil {
		return err
	}
	res, err := p.Run()
	if pipeline.IsInterrupted(err) {
		fmt.Printf("interrupted after stage=%s; state preserved under %s (resume to continue)\n",
			cfg.InterruptAfter, f.out)
		return nil
	}
	if err != nil {
		return err
	}
	printReport(res)
	return nil
}

func runResume(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	// Resume reuses persisted summaries; scenario and gates are read back from
	// stage state, so only --out is required.
	cfg := pipeline.Config{
		OutDir: f.out, Scenario: fixture.ScenHealthy, TargetBitrate: 80_000,
		MinQuality: 0.90, CPUBudgetNS: 1 << 40, ByteBudget: 1 << 40, MaxDeliveryB: 1 << 40,
	}
	_ = cfg
	// Reconstruct the original scenario from the ingest summary artifact.
	scenario, err := detectScenario(f.out)
	if err != nil {
		return err
	}
	cfg.Scenario = scenario
	p, err := pipeline.New(cfg, "demo-run")
	if err != nil {
		return err
	}
	res, err := p.Run()
	if err != nil {
		return err
	}
	printReport(res)
	return nil
}

func runVerify(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	m, err := manifest.Verify(f.out)
	if err != nil {
		return err
	}
	fmt.Println("verify OK")
	fmt.Printf("manifest_sha256=%s schema=%s\n", m.ManifestSHA, m.Schema)
	for _, tr := range m.Tracks {
		fmt.Printf("track=%s frames=%d gaps=%d duplicates=%d duration_ns=%d\n",
			tr.Track, tr.FrameCount, len(tr.Gaps), tr.Duplicates, tr.DurationNS)
	}
	r := m.Quality
	fmt.Printf("algorithm=%s\n", r.Algorithm)
	fmt.Printf("target_bitrate_bps=%d\n", r.TargetBitrateBPS)
	fmt.Printf("actual_bitrate_bps=%d\n", r.ActualBitrateBPS)
	fmt.Printf("bitrate_deviation_pct=%.4f\n", r.BitrateDeviationPCT)
	fmt.Printf("quality_metric=%.4f\n", r.QualityMetric)
	fmt.Printf("manifest_alignment_error_ns=%d\n", r.AlignmentErrorNS)
	if !quality.WithinBitrateGate(r.TargetBitrateBPS, r.ActualBitrateBPS) {
		return model.ErrManifestVerify.At("", 0, "verify", 0,
			"bitrate deviation exceeds 5% gate")
	}
	if r.AlignmentErrorNS > int64(model.Millisecond) {
		return model.ErrManifestVerify.At("", 0, "verify", 0,
			"alignment error exceeds 1ms")
	}
	return nil
}

func printReport(res *pipeline.Result) {
	r := res.Report
	fmt.Printf("algorithm=%s\n", r.Algorithm)
	fmt.Printf("target_bitrate_bps=%d\n", r.TargetBitrateBPS)
	fmt.Printf("actual_bitrate_bps=%d\n", r.ActualBitrateBPS)
	fmt.Printf("bitrate_deviation_pct=%.4f\n", r.BitrateDeviationPCT)
	fmt.Printf("quality_metric=%.4f\n", r.QualityMetric)
	fmt.Printf("manifest_alignment_error_ns=%d\n", r.AlignmentErrorNS)
	for _, tr := range res.Manifest.Tracks {
		fmt.Printf("track=%s frames=%d gaps=%d duplicates=%d action=%s\n",
			tr.Track, tr.FrameCount, len(tr.Gaps), tr.Duplicates, tr.Action)
	}
}
