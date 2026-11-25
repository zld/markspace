// Command replaycheck is the terminal adapter for the replay kernel. It runs
// the six pipeline phases against a generated fixture and reports every result
// as a JSON line, so a shell can assert on exit codes and fields alone.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"replaycheck/internal/fixtures"
	"replaycheck/internal/ingest"
	"replaycheck/internal/model"
	"replaycheck/internal/pipeline"
	"replaycheck/internal/policy"
	"replaycheck/internal/report"
)

const (
	exitOK         = 0
	exitUsage      = 1
	exitProcessing = 2
)

type options struct {
	fixture     string
	workDir     string
	budgetBytes int64
	budgetMS    int64
	qualityMin  string
	resume      bool
	verify      bool
	failStage   string
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}

func run(args []string, stdout *os.File) int {
	out := report.NewWriter(stdout)
	opts, err := parse(args)
	if err != nil {
		out.Emit(report.KindError, map[string]any{"stage": "", "code": exitUsage, "message": err.Error()})
		return exitUsage
	}
	kind := fixtures.Kind(opts.fixture)
	switch kind {
	case fixtures.KindNormal, fixtures.KindTruncated, fixtures.KindWrap, fixtures.KindTimebase, fixtures.KindDuplicate:
	default:
		out.Emit(report.KindError, map[string]any{"stage": "", "code": exitUsage, "message": fmt.Sprintf("unknown fixture %q", opts.fixture)})
		return exitUsage
	}
	quality, err := parseQuality(opts.qualityMin)
	if err != nil {
		out.Emit(report.KindError, map[string]any{"stage": "", "code": exitUsage, "message": err.Error()})
		return exitUsage
	}
	if opts.budgetBytes <= 0 || opts.budgetMS <= 0 {
		out.Emit(report.KindError, map[string]any{"stage": "", "code": exitUsage, "message": "budget values must be positive"})
		return exitUsage
	}
	failAt := model.Stage(opts.failStage)
	if failAt != "" && failAt.Index() < 0 {
		out.Emit(report.KindError, map[string]any{"stage": "", "code": exitUsage, "message": fmt.Sprintf("unknown stage %q", opts.failStage)})
		return exitUsage
	}
	work := opts.workDir
	if work == "" {
		work = filepath.Join(".replaycheck", string(kind))
	}
	cfg := pipeline.Config{
		FixtureDir: filepath.Join(work, "fixtures"),
		SiteDir:    filepath.Join(work, "site"),
		Kind:       kind,
		Budget: policy.Budget{
			MaxBytes:   opts.budgetBytes,
			MaxProcMS:  opts.budgetMS,
			MinQuality: quality,
		},
		Resume: opts.resume,
		Verify: opts.verify,
		Limits: ingest.DefaultLimits(),
		FailAt: failAt,
	}
	// The pipeline emits the structured error line itself, including stage,
	// track and byte offset; the adapter only chooses the exit code.
	res, err := pipeline.Run(cfg, out)
	if err != nil {
		return exitProcessing
	}
	if !res.Valid || res.Segments < 3 || res.MinQualityPPM < quality {
		out.Emit(report.KindError, map[string]any{
			"stage": "manifest", "code": exitProcessing,
			"message": fmt.Sprintf("acceptance failed: valid=%v segments=%d minQuality=%d", res.Valid, res.Segments, res.MinQualityPPM),
		})
		return exitProcessing
	}
	return exitOK
}

// parseQuality converts a decimal quality such as "0.90" into parts per
// million using integer arithmetic, so a threshold never drifts by a float
// rounding error. Up to six decimal places are accepted.
func parseQuality(s string) (policy.PPM, error) {
	if s == "" {
		return 0, fmt.Errorf("quality floor is required")
	}
	whole, frac, scale := int64(0), int64(0), int64(1)
	seenDot, digits := false, 0
	for i, c := range s {
		switch {
		case c == '.' && !seenDot && i > 0:
			seenDot = true
		case c >= '0' && c <= '9':
			if seenDot {
				if digits >= 6 {
					return 0, fmt.Errorf("quality %q has more than six decimal places", s)
				}
				frac = frac*10 + int64(c-'0')
				scale *= 10
				digits++
			} else {
				if whole > 1 {
					return 0, fmt.Errorf("quality %q is out of range", s)
				}
				whole = whole*10 + int64(c-'0')
			}
		default:
			return 0, fmt.Errorf("quality %q is not a decimal number", s)
		}
	}
	ppm := whole*1_000_000 + frac*1_000_000/scale
	if ppm > 1_000_000 {
		return 0, fmt.Errorf("quality %q must be within [0,1]", s)
	}
	return policy.PPM(ppm), nil
}

func parse(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("replaycheck", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&o.fixture, "fixture", "generated", "fixture: generated|normal|truncated|wrap|timebase|duplicate")
	fs.StringVar(&o.workDir, "workdir", "", "working directory for fixtures, cache and state")
	fs.Int64Var(&o.budgetBytes, "budget-bytes", 120000, "maximum delivered bytes")
	fs.Int64Var(&o.budgetMS, "budget-ms", 5000, "maximum processing milliseconds")
	fs.StringVar(&o.qualityMin, "quality-min", "0.90", "minimum delivered quality, e.g. 0.90")
	fs.BoolVar(&o.resume, "resume", false, "resume from the last successful stage")
	fs.BoolVar(&o.verify, "verify-manifest", false, "recompute manifest digests and totals")
	fs.StringVar(&o.failStage, "fail-at", "", "inject a failure before this stage (tests recovery)")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	return o, nil
}
