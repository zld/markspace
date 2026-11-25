// Command tracecheck runs the offline media investigation kernel against a
// self-generated trace and prints a deterministic summary plus a JSON report.
//
// Exit codes:
//
//	0   analysis completed and every configured budget passed
//	2   analysis completed but a business threshold was exceeded
//	3   invalid parameters or unparseable input
//	130 analysis canceled before completion
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"tracecheck/internal/app"
	"tracecheck/internal/fixture"
	"tracecheck/internal/model"
	"tracecheck/internal/report"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

type flags struct {
	generate      bool
	profile       string
	seed          int64
	workers       int
	queue         int
	lossBudget    int
	keyframeBudget int64
	cancelAfter   int
	windowMS      int64
	out           string
	jsonStdout    bool
}

func parseFlags(args []string) (flags, error) {
	fs := flag.NewFlagSet("tracecheck", flag.ContinueOnError)
	var f flags
	fs.BoolVar(&f.generate, "generate", false, "generate and analyze a self-contained sample trace")
	fs.StringVar(&f.profile, "profile", fixture.ProfileClean, "sample profile: clean|damaged|cancel")
	fs.Int64Var(&f.seed, "seed", 1, "deterministic random seed for the generator")
	fs.IntVar(&f.workers, "workers", 2, "bounded worker count")
	fs.IntVar(&f.queue, "queue", 4, "bounded work queue depth")
	fs.IntVar(&f.lossBudget, "loss-budget", 5, "maximum tolerated lost packets")
	fs.Int64Var(&f.keyframeBudget, "keyframe-budget", 3, "maximum keyframe interval in milliseconds")
	fs.IntVar(&f.cancelAfter, "cancel-after", 0, "cancel after N jobs (cancel profile)")
	fs.Int64Var(&f.windowMS, "window-ms", 100, "throughput window size in milliseconds")
	fs.StringVar(&f.out, "out", "", "write JSON report to this path (default: out/<profile>-<seed>.json)")
	fs.BoolVar(&f.jsonStdout, "json", false, "print the full JSON report to stdout")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	return f, validate(f)
}

func validate(f flags) error {
	if !f.generate {
		return fmt.Errorf("only --generate mode is supported offline; pass --generate")
	}
	switch f.profile {
	case fixture.ProfileClean, fixture.ProfileDamaged, fixture.ProfileCancel:
	default:
		return fmt.Errorf("unknown profile %q", f.profile)
	}
	if f.workers < 1 {
		return fmt.Errorf("--workers must be >= 1, got %d", f.workers)
	}
	if f.queue < 1 {
		return fmt.Errorf("--queue must be >= 1, got %d", f.queue)
	}
	if f.lossBudget < 0 {
		return fmt.Errorf("--loss-budget must be >= 0")
	}
	if f.keyframeBudget <= 0 {
		return fmt.Errorf("--keyframe-budget must be > 0 milliseconds")
	}
	if f.windowMS <= 0 {
		return fmt.Errorf("--window-ms must be > 0")
	}
	if f.profile == fixture.ProfileCancel && f.cancelAfter <= 0 {
		return fmt.Errorf("cancel profile requires --cancel-after > 0")
	}
	return nil
}

func run(args []string) int {
	f, err := parseFlags(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tracecheck: %v\n", err)
		return 3
	}

	cfg := app.Config{
		Profile:        f.profile,
		Seed:           f.seed,
		Workers:        f.workers,
		Queue:          f.queue,
		LossBudget:     f.lossBudget,
		KeyframeBudget: model.TicksFromMillis(f.keyframeBudget),
		CancelAfter:    f.cancelAfter,
		WindowTicks:    model.TicksFromMillis(f.windowMS),
	}
	outcome, err := app.Analyze(context.Background(), cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tracecheck: input error: %v\n", err)
		return 3
	}

	reportBytes, err := outcome.Report.JSON()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tracecheck: report error: %v\n", err)
		return 3
	}
	if err := writeReport(f, reportBytes); err != nil {
		fmt.Fprintf(os.Stderr, "tracecheck: %v\n", err)
		return 3
	}

	printSummary(f, outcome, reportBytes)
	return outcome.ExitCode
}

func writeReport(f flags, reportBytes []byte) error {
	path := f.out
	if path == "" {
		path = filepath.Join("out", fmt.Sprintf("%s-%d.json", f.profile, f.seed))
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, reportBytes, 0o644)
}

func printSummary(f flags, outcome *app.Outcome, reportBytes []byte) {
	s := outcome.Report.Summary
	st := outcome.Stats
	fmt.Printf("tracecheck profile=%s seed=%d\n", f.profile, f.seed)
	fmt.Printf("  packets=%d tracks=%d keyframes=%d duplicates=%d loss=%d truncated=%d bad_index=%d\n",
		s.PacketCount, s.TrackCount, s.KeyframeCount, s.DuplicateCount,
		s.LossCount, s.TruncatedCount, s.BadIndexCount)
	fmt.Printf("  loss_budget=%s keyframe_budget=%s\n",
		passFail(s.LossBudgetPassed), passFail(s.KeyframeBudgetPassed))
	fmt.Printf("  intervals=%d conclusions=%d flow_windows=%d\n",
		len(outcome.Report.Intervals), len(outcome.Report.Conclusions), s.FlowWindowCount)
	if s.Canceled {
		fmt.Printf("  canceled: completed=%d not_received=%d submitted=%d max_queue_depth=%d\n",
			st.Completed, st.NotReceived, st.Submitted, st.MaxQueue)
	}
	fmt.Printf("  summary: %s (exit %d)\n", summaryText(s), outcome.ExitCode)
	if f.jsonStdout {
		var pretty map[string]any
		_ = json.Unmarshal(reportBytes, &pretty)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(pretty)
	}
}

func passFail(ok bool) string {
	if ok {
		return "pass"
	}
	return "FAIL"
}

func summaryText(s report.Summary) string {
	if s.Canceled {
		return "canceled"
	}
	if s.Passed {
		return "budget passed"
	}
	return "budget exceeded"
}
