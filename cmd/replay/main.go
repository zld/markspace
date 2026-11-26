// Command replay is the terminal entry point for the highlight-clip
// pipeline. It exposes two subcommands: "fixtures" generates the local
// test data and "run" executes the scan/plan/cut/commit stages.
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"replay/internal/clock"
	"replay/internal/events"
	"replay/internal/manifest"
	"replay/internal/pipeline"
	"replay/internal/testkit"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "fixtures":
		runFixtures(os.Args[2:])
	case "run":
		code := runPipeline(os.Args[2:])
		os.Exit(code)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `replay - event-aligned MP4 clip pipeline

  replay fixtures --out DIR
  replay run --input VIDEO --events JSONL --out DIR --cache DIR
             --rtp-clock HZ --rtp-anchor RTPTICK:MEDIATICK
             --pre DURATION --post DURATION --min-confidence F
             --duration-budget DURATION [--stop-after STAGE]

exit codes: 0 completed (skips recorded), 1 config/conflict,
            2 truncated/malformed container, 75 deliberate stop-after
`)
}

func runFixtures(args []string) {
	fs := flag.NewFlagSet("fixtures", flag.ContinueOnError)
	out := fs.String("out", "", "output directory for fixtures")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "fixtures: --out is required")
		os.Exit(1)
	}
	if err := testkit.Generate(*out); err != nil {
		fmt.Fprintf(os.Stderr, "fixtures: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("fixtures written to %s\n", *out)
}

func runPipeline(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	cfg := &pipeline.Config{}
	var anchor string
	var rtpClock int
	fs.StringVar(&cfg.Input, "input", "", "source MP4/MOV")
	fs.StringVar(&cfg.Events, "events", "", "JSONL event feed")
	fs.StringVar(&cfg.OutDir, "out", "", "output directory")
	fs.StringVar(&cfg.CacheDir, "cache", "", "cache directory")
	fs.IntVar(&rtpClock, "rtp-clock", 0, "RTP clock frequency in Hz")
	fs.StringVar(&anchor, "rtp-anchor", "", "RTP tick:media tick anchor")
	fs.DurationVar(&cfg.Pre, "pre", 0, "lead-in before event time")
	fs.DurationVar(&cfg.Post, "post", 0, "lead-out after event window")
	fs.Float64Var(&cfg.MinConfidence, "min-confidence", 0.6, "confidence threshold")
	fs.DurationVar(&cfg.Budget, "duration-budget", 0, "max aligned clip duration")
	fs.StringVar(&cfg.StopAfter, "stop-after", "", "stop after scan|plan|cut")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	cfg.RTPClock = uint32(rtpClock)
	a, err := parseAnchor(anchor)
	if err != nil {
		fmt.Fprintf(os.Stderr, "run: %v\n", err)
		return 1
	}
	cfg.RTPAnchor = a

	st, mf, err := pipeline.Run(cfg)
	if err != nil {
		return classifyError(err)
	}
	if st != nil {
		printStats(st, mf)
	}
	return 0
}

// parseAnchor accepts the RTPTICK:MEDIATICK form, both as integers.
func parseAnchor(s string) (events.Anchor, error) {
	if s == "" {
		return events.Anchor{}, clock.NewError(clock.KindConfig, "anchor",
			"--rtp-anchor is required")
	}
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return events.Anchor{}, clock.NewError(clock.KindConfig, "anchor",
			"anchor must look like 4294787296:0")
	}
	rtpTick, err := strconv.ParseUint(parts[0], 10, 32)
	if err != nil {
		return events.Anchor{}, clock.NewError(clock.KindConfig, "anchor",
			"invalid RTP tick: "+err.Error())
	}
	mediaTick, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return events.Anchor{}, clock.NewError(clock.KindConfig, "anchor",
			"invalid media tick: "+err.Error())
	}
	return events.Anchor{RtpTick: uint32(rtpTick), MediaTick: mediaTick}, nil
}

// classifyError maps structured errors to the documented exit codes.
func classifyError(err error) int {
	if se, ok := err.(*pipeline.StopAfter); ok {
		fmt.Printf("stop_after=%s\n", se.Stage())
		return 75
	}
	if ce, ok := clock.AsClockError(err); ok {
		switch ce.Kind {
		case clock.KindBoxTruncated:
			fmt.Fprintf(os.Stderr, "box_truncated offset=%d: %s\n", ce.Offset, ce.Error())
			return 2
		case clock.KindConfig, clock.KindConflict:
			fmt.Fprintf(os.Stderr, "%s\n", ce.Error())
			return 1
		}
	}
	fmt.Fprintf(os.Stderr, "run failed: %v\n", err)
	return 1
}

// printStats emits one grep-friendly key=value line per acceptance metric.
func printStats(st *pipeline.Stats, mf *manifest.Manifest) {
	lines := [][2]string{
		{"accepted", strconv.Itoa(st.Accepted)},
		{"duplicate", strconv.Itoa(st.Duplicate)},
		{"skip_budget", strconv.Itoa(st.SkipBudget)},
		{"skip_confidence", strconv.Itoa(st.SkipConfidence)},
		{"skip_untrusted", strconv.Itoa(st.SkipUntrusted)},
		{"no_keyframe", strconv.Itoa(st.NoKeyframe)},
		{"rtp_bad", strconv.Itoa(st.RTPBad)},
		{"rtp_wraps", strconv.Itoa(st.RTPWraps)},
		{"anomaly_intervals", strconv.Itoa(st.AnomalyCount)},
		{"affected", strconv.Itoa(st.Affected)},
		{"clip_s", formatSeconds(st.ClipS)},
		{"budget_s", formatSeconds(st.BudgetS)},
		{"deviation_s", formatSeconds(st.DeviationS)},
		{"cache_hits", strconv.Itoa(st.CacheHits)},
		{"cache_rebuilds", strconv.Itoa(st.CacheRebuilds)},
	}
	for _, kv := range lines {
		fmt.Printf("%s=%s\n", kv[0], kv[1])
	}
	if mf != nil {
		fmt.Printf("manifest_sha256=%s\n", mf.SHA)
	}
}

// formatSeconds prints whole-second metrics without a trailing decimal
// when the value is integral, matching the acceptance report format.
func formatSeconds(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', 3, 64)
}
