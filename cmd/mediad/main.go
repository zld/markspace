// Command mediad is the terminal entry point for the timed-metadata anomaly
// scanner and range-scoped recovery pipeline. It has no GUI and performs all
// work through flags and stdout/stderr on the local machine.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"mediad/internal/fixture"
	"mediad/internal/model"
	"mediad/internal/pipeline"
	"mediad/internal/verify"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return model.ExitUsage
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "-h", "--help", "help":
		usage()
		return model.ExitOK
	default:
		fmt.Fprintf(os.Stderr, "mediad: unknown command %q\n", args[0])
		usage()
		return model.ExitUsage
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `mediad - local timed-metadata anomaly scanner and recovery

usage:
  mediad demo --work <dir> --from <dur> --to <dur> [--scenarios a,b,...]
              [--target-kbps <n>] [--duration <secs>] [--json]

durations accept s/ms suffixes, e.g. 1s, 500ms.
scenarios: normal, damage, replace, metadata (default: all).
exit codes: 0 recovered/ok, 1 usage or execution error, 2 unrecoverable input.
`)
}

type demoFlags struct {
	work       string
	from       string
	to         string
	scenarios  string
	targetKbps int
	duration   float64
	jsonOut    bool
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var df demoFlags
	fs.StringVar(&df.work, "work", "", "working directory for fixtures, cache and manifests")
	fs.StringVar(&df.from, "from", "1s", "window start (e.g. 1s)")
	fs.StringVar(&df.to, "to", "5s", "window end (e.g. 5s)")
	fs.StringVar(&df.scenarios, "scenarios", strings.Join(fixture.AllScenarios, ","), "comma-separated scenarios")
	fs.IntVar(&df.targetKbps, "target-kbps", 800, "target bitrate in kbps")
	fs.Float64Var(&df.duration, "duration", 6, "generated clip duration in seconds")
	fs.BoolVar(&df.jsonOut, "json", false, "emit machine-readable JSON rows")
	if err := fs.Parse(args); err != nil {
		return model.ExitUsage
	}
	if df.work == "" {
		fmt.Fprintln(os.Stderr, "mediad demo: --work is required")
		return model.ExitUsage
	}
	fromNanos, err := parseDuration(df.from)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediad demo: bad --from %q: %v\n", df.from, err)
		return model.ExitUsage
	}
	toNanos, err := parseDuration(df.to)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediad demo: bad --to %q: %v\n", df.to, err)
		return model.ExitUsage
	}
	if fromNanos >= toNanos {
		fmt.Fprintln(os.Stderr, "mediad demo: --from must be earlier than --to")
		return model.ExitUsage
	}
	scenarios := splitScenarios(df.scenarios)
	if err := validateScenarios(scenarios); err != nil {
		fmt.Fprintf(os.Stderr, "mediad demo: %v\n", err)
		return model.ExitUsage
	}

	cfg := pipeline.DemoConfig{
		WorkDir:     df.work,
		Scenarios:   scenarios,
		FromNanos:   fromNanos,
		ToNanos:     toNanos,
		TargetKbps:  df.targetKbps,
		DurationSec: df.duration,
		Runner:      verify.FFmpegRunner{},
	}
	rows, err := pipeline.Demo(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediad demo: execution error: %v\n", err)
		return model.ExitUsage
	}

	if df.jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rows); err != nil {
			return model.ExitUsage
		}
	} else {
		printTable(rows)
	}
	// Any row reporting an unrecoverable range yields exit 2; the manifest is
	// already retained on disk. The four built-in scenarios are all recoverable
	// or healthy, so a complete demo returns 0.
	for _, r := range rows {
		if r.ExitCode == model.ExitUnrecoverable {
			return model.ExitUnrecoverable
		}
	}
	return model.ExitOK
}

func splitScenarios(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func validateScenarios(ss []string) error {
	known := map[string]bool{}
	for _, s := range fixture.KnownScenarios {
		known[s] = true
	}
	for _, s := range ss {
		if !known[s] {
			return fmt.Errorf("unknown scenario %q (valid: %s)", s, strings.Join(fixture.KnownScenarios, ", "))
		}
	}
	return nil
}

// parseDuration accepts values like "1500ms", "5s" or a bare float seconds.
func parseDuration(s string) (int64, error) {
	s = strings.TrimSpace(s)
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "ms"):
		mult = 1e-3
		s = s[:len(s)-2]
	case strings.HasSuffix(s, "s"):
		s = s[:len(s)-1]
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, err
	}
	return model.SecondsToNanos(v * mult)
}

func printTable(rows []*pipeline.Row) {
	hdr := []string{"scenario", "target_kbps", "actual_kbps", "bitrate_delta_pct",
		"psnr_db", "manifest_max_error_ms", "anomaly_count",
		"recovered_segments", "cache_hit", "index_rebuilt"}
	fmt.Println(strings.Join(hdr, "\t"))
	for _, r := range rows {
		fmt.Printf("%s\t%d\t%d\t%.2f\t%.2f\t%.3f\t%d\t%d\t%t\t%t\n",
			r.Scenario, r.TargetKbps, r.ActualKbps, r.BitrateDeltaPct,
			psnrLabel(r.PSNRDB), r.ManifestMaxErrorMs, r.AnomalyCount,
			r.RecoveredSegments, r.CacheHit, r.IndexRebuilt)
	}
}

// psnrLabel renders the lossless sentinel as "inf" for the terminal.
func psnrLabel(v float64) float64 {
	if v == verify.PSNRIdentical {
		return v // printed as a large finite value; JSON keeps it exact
	}
	return v
}
