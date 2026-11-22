// Command mediapipeline is the terminal front end for the media pipeline.
//
// Exit status: 0 when every invariant holds, 2 when input was corrupt and the
// corruption was recorded, 1 for an internal error or a failed invariant.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"mediapipeline/internal/media"
	"mediapipeline/internal/pipeline"
)

const usage = `mediapipeline — deterministic segmented media pipeline

Usage:
  mediapipeline demo   [flags]
  mediapipeline verify [flags]

demo flags:
  --seed N              deterministic seed (default 7)
  --duration D          content duration, e.g. 12s (default 12s)
  --segment D           target segment duration, e.g. 2s (default 2s)
  --bitrates LIST       comma-separated kbps values (default 400,900,1600)
  --loss F              packet loss probability in [0,1] (default 0.03)
  --jitter D            network jitter, e.g. 80ms (default 80ms)
  --late D              allowed lateness / late-event tolerance (default 250ms)
  --interrupt-after N   simulate a crash after N segments per representation
  --workdir PATH        output directory (default .mediapipeline)

verify flags:
  --seed N              deterministic seed (default 7)
  --case LIST           comma-separated fault cases
                        (truncated,zero-timescale,late-event,sequence-wrap,
                         burst-loss,corrupt-sample-table,out-of-order-pts)
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 1
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "verify":
		return runVerify(args[1:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", args[0], usage)
		return 1
	}
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	seed := fs.Int64("seed", 7, "deterministic seed")
	duration := fs.String("duration", "12s", "content duration")
	segment := fs.String("segment", "2s", "target segment duration")
	bitrates := fs.String("bitrates", "400,900,1600", "comma-separated kbps values")
	loss := fs.Float64("loss", 0.03, "packet loss probability")
	jitter := fs.String("jitter", "80ms", "network jitter")
	late := fs.String("late", "250ms", "allowed lateness")
	interruptAfter := fs.Int("interrupt-after", 0, "simulate interruption after N segments")
	workdir := fs.String("workdir", ".mediapipeline", "output directory")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	dur, err := parseDuration(*duration)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --duration: %v\n", err)
		return 1
	}
	seg, err := parseDuration(*segment)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --segment: %v\n", err)
		return 1
	}
	jit, err := parseNonNegativeDuration(*jitter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --jitter: %v\n", err)
		return 1
	}
	lat, err := parseNonNegativeDuration(*late)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --late: %v\n", err)
		return 1
	}
	rates, err := parseBitrates(*bitrates)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --bitrates: %v\n", err)
		return 1
	}
	opts := pipeline.Options{
		Seed:           *seed,
		Duration:       dur,
		Segment:        seg,
		Bitrates:       rates,
		Loss:           *loss,
		Jitter:         jit,
		Late:           lat,
		InterruptAfter: *interruptAfter,
		WorkDir:        *workdir,
	}
	res, err := pipeline.Run(opts)
	if err != nil {
		if media.IsCorruptInput(err) {
			fmt.Fprintf(os.Stderr, "input error: %v\n", err)
			return 2
		}
		fmt.Fprintf(os.Stderr, "internal error: %v\n", err)
		return 1
	}
	printDemo(res)
	if !demoInvariantsHold(res) {
		return 1
	}
	return 0
}

// demoInvariantsHold encodes the acceptance thresholds documented for demo.
func demoInvariantsHold(r *pipeline.Result) bool {
	ok := true
	check := func(name string, pass bool, detail string) {
		if !pass {
			ok = false
			fmt.Fprintf(os.Stderr, "invariant failed: %s (%s)\n", name, detail)
		}
	}
	check("av_sync_max_ms<=40", r.AVSyncMaxMS <= 40, fmt.Sprintf("%.2f", r.AVSyncMaxMS))
	// A total-loss channel genuinely fails this quality bar; passing it
	// silently would misrepresent the run.
	check("packet_recovery_rate>=0.85", r.PacketRecoveryRate >= 0.85,
		fmt.Sprintf("%.3f (%d received)", r.PacketRecoveryRate, r.RecvStats.Received))
	check("p95_latency_ms<=250", r.P95LatencyMS <= 250, fmt.Sprintf("%.2f", r.P95LatencyMS))
	check("resume_outputs", r.ResumeOutputsEqual, fmt.Sprintf("%t", r.ResumeOutputsEqual))
	check("aligned_boundaries", r.Segments > 0, fmt.Sprintf("%d", r.Segments))
	return ok
}

func printDemo(r *pipeline.Result) {
	fmt.Printf("av_sync_max_ms=%s\n", formatFloat(r.AVSyncMaxMS))
	fmt.Printf("packet_recovery_rate=%.4f\n", r.PacketRecoveryRate)
	fmt.Printf("p95_latency_ms=%s\n", formatFloat(r.P95LatencyMS))
	fmt.Printf("aligned_boundaries=%d\n", r.AlignedBoundaries)
	fmt.Printf("representations=%d\n", r.Representations)
	fmt.Printf("segments=%d\n", r.Segments)
	fmt.Printf("late_events=%d\n", r.LateEvents)
	for _, iv := range r.ReplayIntervals {
		fmt.Printf("replay_interval=[%dms,%dms)\n", int64(iv.Start)/1e6, int64(iv.End)/1e6)
	}
	fmt.Printf("packets_received=%d delivered=%d lost=%d duplicates=%d reordered=%d wraps=%d\n",
		r.RecvStats.Received, r.RecvStats.Delivered, r.RecvStats.Lost,
		r.RecvStats.Duplicates, r.RecvStats.Reordered, r.RecvStats.Wraps)
	fmt.Printf("resume_outputs=%t reused=%d written=%d interrupted=%t\n",
		r.ResumeOutputsEqual, r.ReusedOutputs, r.WrittenOutputs, r.Interrupted)
	fmt.Printf("provenance_records=%d\n", r.ProvenanceRecords)
	fmt.Printf("manifest_sha256=%s\n", r.ManifestSHA256)
}

func runVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	seed := fs.Int64("seed", 7, "deterministic seed")
	defaults := make([]string, 0, 7)
	for _, f := range pipeline.AllFaults() {
		defaults = append(defaults, string(f))
	}
	cases := fs.String("case", strings.Join(defaults, ","), "fault cases")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	kinds, err := pipeline.ParseFaults(*cases)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --case: %v\n", err)
		return 1
	}
	report, err := pipeline.Verify(*seed, kinds)
	if err != nil {
		fmt.Fprintf(os.Stderr, "internal error: %v\n", err)
		return 1
	}
	for _, line := range report.Lines() {
		fmt.Println(line)
	}
	counts := report.FaultCounts()
	cats := make([]string, 0, len(counts))
	for c := range counts {
		cats = append(cats, string(c))
	}
	sort.Strings(cats)
	for _, c := range cats {
		fmt.Printf("error_count[%s]=%d\n", c, counts[media.Category(c)])
	}
	kindCounts := report.KindCounts()
	for _, k := range pipeline.AllFaults() {
		if c, ok := kindCounts[k]; ok {
			fmt.Printf("fault_count[%s]=%d\n", k, c)
		}
	}
	for _, span := range report.ReplaySpans() {
		fmt.Printf("replay_interval=%s\n", span)
	}
	fmt.Printf("seed=%d cases=%d\n", *seed, len(kinds))
	return report.ExitCode()
}

// parseDuration accepts Go durations plus a plain millisecond count and
// requires a strictly positive value.
func parseDuration(s string) (media.Nano, error) {
	d, err := parseRawDuration(s)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("must be positive")
	}
	return media.Nano(d), nil
}

// parseNonNegativeDuration is parseDuration without the positivity check:
// zero jitter and zero allowed lateness are meaningful configurations.
func parseNonNegativeDuration(s string) (media.Nano, error) {
	d, err := parseRawDuration(s)
	if err != nil {
		return 0, err
	}
	if d < 0 {
		return 0, fmt.Errorf("must not be negative")
	}
	return media.Nano(d), nil
}

func parseRawDuration(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		ms, convErr := strconv.ParseInt(s, 10, 64)
		if convErr != nil {
			return 0, err
		}
		d = time.Duration(ms) * time.Millisecond
	}
	return d, nil
}

// parseBitrates parses a comma-separated list of positive kbps values.
func parseBitrates(s string) ([]int, error) {
	var out []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := strconv.Atoi(part)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("invalid bitrate %q", part)
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no bitrates given")
	}
	return out, nil
}

// formatFloat trims trailing zeros so output is stable across runs.
func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64)
}
