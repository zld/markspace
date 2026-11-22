// Command medialab runs the self-contained media processing demo: it
// generates multi-track fixtures, injects link faults, recovers and
// synthesizes a shared timeline, exports hashed off-screen frames and writes
// a machine-readable summary.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/medialab/medialab/internal/demo"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return demo.ExitInputErr
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "-h", "--help", "help":
		usage()
		return demo.ExitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		usage()
		return demo.ExitInputErr
	}
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	seed := fs.Int64("seed", 42, "deterministic random seed")
	duration := fs.String("duration", "7200s", "material duration (e.g. 7200s)")
	tracks := fs.Int("tracks", 3, "number of tracks (one video plus audio)")
	lossBurst := fs.Int("loss-burst", 7, "consecutive lost packets that form a gap")
	window := fs.Int("window", 64, "reorder window capacity in packets")
	queue := fs.Int("queue", 8, "bounded pipeline queue capacity")
	targetBps := fs.Int64("target-bps", 2_500_000, "service target bitrate in bps")
	out := fs.String("out", "./artifacts", "output directory")
	faultCase := fs.String("case", "normal", "fault scenario: normal|corrupt|truncated|checksum|late|burst|bandwidth|render")
	if err := fs.Parse(args); err != nil {
		return demo.ExitInputErr
	}

	durSec, err := parseDuration(*duration)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --duration: %v\n", err)
		return demo.ExitInputErr
	}
	fault, err := parseFault(*faultCase)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --case: %v\n", err)
		return demo.ExitInputErr
	}

	cfg := demo.Config{
		Seed: *seed, DurationSec: durSec, Tracks: *tracks,
		LossBurst: *lossBurst, WindowCapacity: *window, QueueCapacity: *queue,
		TargetBps: *targetBps, OutDir: *out, Fault: fault,
	}
	res := demo.Run(context.Background(), cfg)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(res.Summary); err != nil {
		fmt.Fprintf(os.Stderr, "encode summary: %v\n", err)
		return demo.ExitProcessErr
	}
	if res.FailedStage != "" {
		fmt.Fprintf(os.Stderr, "stage=%s error=%v\n", res.FailedStage, res.Err)
	}
	return res.ExitCode
}

func parseDuration(s string) (int64, error) {
	if len(s) < 2 || s[len(s)-1] != 's' {
		return 0, fmt.Errorf("duration must end in 's', got %q", s)
	}
	var v int64
	for _, ch := range s[:len(s)-1] {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("duration must be whole seconds, got %q", s)
		}
		v = v*10 + int64(ch-'0')
	}
	if v <= 0 {
		return 0, fmt.Errorf("duration must be positive, got %q", s)
	}
	return v, nil
}

func parseFault(s string) (demo.Fault, error) {
	switch s {
	case "normal":
		return demo.FaultNone, nil
	case "corrupt":
		return demo.FaultComposite, nil
	case "truncated", "checksum", "late", "burst", "bandwidth", "render":
		return demo.Fault(s), nil
	default:
		return "", fmt.Errorf("unknown case %q", s)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `medialab - deterministic media recovery and evidence export

usage:
  medialab demo [flags]

flags:
  --seed int          deterministic random seed (default 42)
  --duration 7200s    material duration in whole seconds
  --tracks int        track count, one video plus audio tracks
  --loss-burst int    consecutive lost packets forming a gap
  --window int        reorder window capacity
  --queue int         bounded pipeline queue capacity
  --target-bps int    service target bitrate
  --out string        output directory
  --case string       normal|corrupt|truncated|checksum|late|burst|bandwidth|render

exit codes: 0 ok, 1 processing/output error, 2 input/manifest error
`)
}
